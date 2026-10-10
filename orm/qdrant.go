package orm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// QdrantStore runs the typed builder against a Qdrant vector database, over
// its REST API — no client library dependency. Collections are named after
// the table; payload fields are the columns; an orm.Vector column becomes
// the collection's (unnamed) vector, and Nearest ordering becomes a search.
//
//	orm.OpenQdrant("qdrant", "http://localhost:6333")
//	s.EnsureCollection(ctx, "docs", 384, orm.Cosine)
//
//	//orm:table docs connection=qdrant key_type=uuid
//	type Doc struct {
//		orm.Model
//		ID    string         `db:"id"`
//		Title string         `db:"title"`
//		Vec   orm.Vector[384] `db:"vec"`
//	}
//	docs, err := Docs.Query().OrderBy(Docs.Vec.Nearest(q, orm.Cosine)).Limit(10).Get(ctx)
type QdrantStore struct {
	base  string
	http  *http.Client
	keyOf func(table string) string
}

// OpenQdrant connects to a Qdrant REST endpoint and registers the
// connection. Keys are UUID strings by default; use WithKey to map another
// column.
func OpenQdrant(name, baseURL string) (*QdrantStore, error) {
	s := &QdrantStore{
		base:  strings.TrimSuffix(baseURL, "/"),
		http:  &http.Client{Timeout: 30 * time.Second},
		keyOf: func(string) string { return "id" },
	}
	AddDocStore(name, s)
	return s, nil
}

// WithKey names the column whose value is the point id (default "id").
func (s *QdrantStore) WithKey(col string) *QdrantStore {
	s.keyOf = func(string) string { return col }
	return s
}

// qdrantHTTPError is a non-2xx REST response, kept typed so callers match
// on the status instead of the message text.
type qdrantHTTPError struct {
	method string
	path   string
	status int
	body   string
}

func (e *qdrantHTTPError) Error() string {
	return fmt.Sprintf("orm: qdrant %s %s: %d: %s", e.method, e.path, e.status, e.body)
}

// qdrantStatus reports err's HTTP status when err is a Qdrant REST error.
func qdrantStatus(err error) (int, bool) {
	if e, ok := errors.AsType[*qdrantHTTPError](err); ok {
		return e.status, true
	}
	return 0, false
}

// EnsureCollection creates the collection if it is missing, with the
// given vector size and distance metric — the migration equivalent for
// Qdrant. Vector columns must match this size.
func (s *QdrantStore) EnsureCollection(ctx context.Context, table string, dim int, metric VectorMetric) error {
	body := map[string]any{
		"vectors": map[string]any{
			"size":     dim,
			"distance": map[VectorMetric]string{L2: "Euclid", Cosine: "Cosine", InnerProduct: "Dot"}[metric],
		},
	}
	err := s.do(ctx, http.MethodPut, "/collections/"+table, body, nil)
	if status, ok := qdrantStatus(err); ok && status == http.StatusConflict {
		return nil // already exists
	}
	return err
}

// DropCollection removes a collection (dropIfExists in a migration).
func (s *QdrantStore) DropCollection(ctx context.Context, table string) error {
	err := s.do(ctx, http.MethodDelete, "/collections/"+table, nil, nil)
	if status, ok := qdrantStatus(err); ok && status == http.StatusNotFound {
		return nil
	}
	return err
}

func (s *QdrantStore) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return &qdrantHTTPError{method: method, path: path, status: res.StatusCode, body: string(b)}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

type qdrantResult[T any] struct {
	Result T `json:"result"`
}

func (s *QdrantStore) key(table string) string { return s.keyOf(table) }

func (s *QdrantStore) FindDocs(ctx context.Context, table string, p Plan) ([]map[string]any, error) {
	filter, err := qdrantFilter(p.Where)
	if err != nil {
		return nil, err
	}
	if p.Vector != nil {
		limit := 10
		if p.Limit > 0 {
			limit = p.Limit
		}
		body := map[string]any{
			"vector":       p.Vector.Floats,
			"limit":        limit,
			"with_payload": true,
		}
		if len(filter) > 0 {
			body["filter"] = filter
		}
		if p.Offset > 0 {
			body["offset"] = p.Offset
		}
		var res qdrantResult[[]qdrantPoint]
		if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/search", body, &res); err != nil {
			return nil, err
		}
		return pointsToDocs(res.Result, s.key(table)), nil
	}
	if len(p.Orders) > 1 {
		return nil, &ErrNotTranslatable{Reason: "more than one order (Qdrant orders by payload field or by vector)"}
	}
	body := map[string]any{"limit": scrollPage(p), "with_payload": true, "with_vector": false}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	if len(p.Orders) == 1 {
		if p.Orders[0].Desc {
			return nil, &ErrNotTranslatable{Reason: "descending order (Qdrant order_by is ascending; store a negated field to sort descending)"}
		}
		body["order_by"] = p.Orders[0].Column
	}
	var docs []map[string]any
	offset := p.Offset
	for {
		if offset > 0 {
			body["offset"] = offset
		}
		var res qdrantResult[struct {
			Points []qdrantPoint `json:"points"`
			Next   *uint64       `json:"next_page_offset"`
		}]
		if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/scroll", body, &res); err != nil {
			return nil, err
		}
		docs = append(docs, pointsToDocs(res.Result.Points, s.key(table))...)
		if res.Result.Next == nil || (p.Limit > 0 && len(docs) >= p.Limit) {
			break
		}
		if next := *res.Result.Next; next <= math.MaxInt32 {
			offset = int(next)
		}
	}
	if p.Limit > 0 && len(docs) > p.Limit {
		docs = docs[:p.Limit]
	}
	if len(p.Columns) > 0 {
		keep := map[string]bool{}
		for _, c := range p.Columns {
			keep[c] = true
		}
		for _, d := range docs {
			for k := range d {
				if !keep[k] {
					delete(d, k)
				}
			}
		}
	}
	return docs, nil
}

type qdrantPoint struct {
	ID      any            `json:"id"`
	Payload map[string]any `json:"payload"`
}

func pointsToDocs(points []qdrantPoint, key string) []map[string]any {
	docs := make([]map[string]any, len(points))
	for i, pt := range points {
		doc := pt.Payload
		if doc == nil {
			doc = map[string]any{}
		}
		if _, ok := doc[key]; !ok {
			doc[key] = pt.ID
		}
		docs[i] = doc
	}
	return docs
}

func scrollPage(p Plan) int {
	if p.Limit > 0 {
		return p.Limit
	}
	return 100
}

func (s *QdrantStore) InsertDocs(ctx context.Context, table string, docs []map[string]any) error {
	key := s.key(table)
	points := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		id, ok := doc[key]
		if !ok || isZero(id) {
			return fmt.Errorf("orm: qdrant insert into %s: every point needs a key in %q", table, key)
		}
		payload := map[string]any{}
		var vector []float64
		for k, v := range doc {
			// Every column lands in the payload, the key and the vector
			// included: filters match payload fields, not point metadata, so
			// documents must be self-contained to be queryable after a read.
			payload[k] = jsonValue(v)
			if f, ok := vectorOf(v); ok {
				vector = f
			}
		}
		pt := map[string]any{"id": id, "payload": payload}
		if vector != nil {
			pt["vector"] = vector
		}
		points = append(points, pt)
	}
	if len(points) == 0 {
		return nil
	}
	return s.do(ctx, http.MethodPut, "/collections/"+table+"/points?wait=true",
		map[string]any{"points": points}, nil)
}

// UpsertDocs overwrites the matching points: the PUT points endpoint is
// idempotent on the point id, which is the document key.
func (s *QdrantStore) UpsertDocs(ctx context.Context, table string, docs []map[string]any, keys []string) error {
	return s.InsertDocs(ctx, table, docs)
}

func (s *QdrantStore) UpdateDocs(ctx context.Context, table string, p Plan, sets map[string]any) (int64, error) {
	if p.Vector != nil {
		return 0, &ErrNotTranslatable{Reason: "updating by vector search"}
	}
	filter, err := qdrantFilter(p.Where)
	if err != nil {
		return 0, err
	}
	n, err := s.CountDocs(ctx, table, p)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{}
	var nils []string
	for k, v := range sets {
		if v == nil {
			nils = append(nils, k)
			continue
		}
		payload[k] = jsonValue(v)
	}
	body := map[string]any{"payload": payload, "wait": true}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/payload", body, nil); err != nil {
		return 0, err
	}
	for _, k := range nils {
		body := map[string]any{"keys": []string{k}, "wait": true}
		if len(filter) > 0 {
			body["filter"] = filter
		}
		if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/payload/delete", body, nil); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (s *QdrantStore) DeleteDocs(ctx context.Context, table string, p Plan) (int64, error) {
	if p.Vector != nil {
		return 0, &ErrNotTranslatable{Reason: "deleting by vector search"}
	}
	filter, err := qdrantFilter(p.Where)
	if err != nil {
		return 0, err
	}
	n, err := s.CountDocs(ctx, table, p)
	if err != nil {
		return 0, err
	}
	body := map[string]any{"wait": true}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/delete", body, nil); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *QdrantStore) CountDocs(ctx context.Context, table string, p Plan) (int64, error) {
	filter, err := qdrantFilter(p.Where)
	if err != nil {
		return 0, err
	}
	body := map[string]any{"exact": true}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	var res qdrantResult[struct {
		Count int64 `json:"count"`
	}]
	if err := s.do(ctx, http.MethodPost, "/collections/"+table+"/points/count", body, &res); err != nil {
		return 0, err
	}
	return res.Result.Count, nil
}

// vectorOf reports v's components when v is a vector value.
func vectorOf(v any) ([]float64, bool) {
	if vv, ok := v.(interface {
		Floats() []float64
	}); ok {
		return vv.Floats(), vv.Floats() != nil
	}
	return nil, false
}

// jsonValue converts a value to its JSON representation for payloads.
func jsonValue(v any) any {
	switch v.(type) {
	case string, bool, int, int32, int64, uint, uint32, uint64, float64, []any, map[string]any:
		return v
	}
	if val, ok := v.(driver.Valuer); ok {
		if raw, err := val.Value(); err == nil && raw != nil {
			if s, isStr := raw.(string); isStr {
				return s
			}
			return raw
		}
	}
	if tm, ok := v.(time.Time); ok {
		return tm.Format(time.RFC3339Nano)
	}
	return v
}

// qdrantFilter compiles a plan condition to a Qdrant filter object. Each
// condition lands in must / must_not / should; the shapes Qdrant cannot
// express (LIKE, deep nesting) are refused.
func qdrantFilter(n Node) (map[string]any, error) {
	if n == nil {
		return nil, nil
	}
	conds, err := qdrantConds(n)
	if err != nil {
		return nil, err
	}
	buckets := map[string][]map[string]any{"must": {}, "must_not": {}, "should": {}}
	for _, c := range conds {
		buckets[c.bucket] = append(buckets[c.bucket], c.cond)
	}
	f := map[string]any{}
	for kind, list := range buckets {
		if len(list) > 0 {
			f[kind] = list
		}
	}
	return f, nil
}

type qdrantCond struct {
	bucket string
	cond   map[string]any
}

func qdrantConds(n Node) ([]qdrantCond, error) {
	switch c := n.(type) {
	case Field:
		v := jsonValue(c.Value)
		switch c.Op {
		case "=":
			return []qdrantCond{{"must", matchCond(c.Column, v)}}, nil
		case "<>", "!=":
			return []qdrantCond{{"must_not", matchCond(c.Column, v)}}, nil
		case ">", ">=", "<", "<=":
			return []qdrantCond{{"must", map[string]any{"key": c.Column, "range": map[string]any{rangeOp(c.Op): v}}}}, nil
		}
		return nil, &ErrNotTranslatable{Reason: "operator " + c.Op + " on Qdrant"}
	case List:
		vals := make([]any, len(c.Values))
		for i, v := range c.Values {
			vals[i] = jsonValue(v)
		}
		bucket := "must"
		if c.Not {
			bucket = "must_not"
		}
		return []qdrantCond{{bucket, map[string]any{"key": c.Column, "match": map[string]any{"any": vals}}}}, nil
	case Range:
		bucket := "must"
		if c.Not {
			bucket = "must_not"
		}
		return []qdrantCond{{bucket, map[string]any{"key": c.Column, "range": map[string]any{
			"gte": jsonValue(c.Lo), "lte": jsonValue(c.Hi),
		}}}}, nil
	case NullTest:
		if c.Not {
			return []qdrantCond{{"must_not", map[string]any{"key": c.Column, "is_null": true}}}, nil
		}
		return []qdrantCond{{"must", map[string]any{"key": c.Column, "is_null": true}}}, nil
	case Composite:
		parts := make([]qdrantCond, 0)
		for _, p := range c.Parts {
			sub, err := qdrantConds(p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, sub...)
		}
		if c.Or {
			// should: any single condition matching satisfies the filter.
			for i := range parts {
				parts[i].bucket = "should"
			}
			return parts, nil
		}
		return parts, nil
	case Negate:
		inner, err := qdrantConds(c.Part)
		if err != nil {
			return nil, err
		}
		if len(inner) != 1 {
			return nil, &ErrNotTranslatable{Reason: "negating a compound condition on Qdrant"}
		}
		inner[0].bucket = map[string]string{"must": "must_not", "must_not": "must", "should": "must_not"}[inner[0].bucket]
		return inner, nil
	case notTranslatable:
		return nil, &ErrNotTranslatable{Reason: "conditions on " + c.reason + " on Qdrant"}
	}
	return nil, &ErrNotTranslatable{Reason: "this condition on Qdrant"}
}

func matchCond(column string, v any) map[string]any {
	return map[string]any{"key": column, "match": map[string]any{"value": v}}
}

func rangeOp(op string) string {
	return map[string]string{">": "gt", ">=": "gte", "<": "lt", "<=": "lte"}[op]
}
