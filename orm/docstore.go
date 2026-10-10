package orm

import (
	"context"
	"fmt"
	"strings"
)

// DocStore is a non-SQL backend behind the same builder: MongoDB and Qdrant
// implement it, and every typed query method runs against it unchanged
// when its connection is a document store. Methods that have no translation
// (joins, unions, raw SQL) fail with ErrNotTranslatable naming the part.
type DocStore interface {
	// FindDocs returns documents (column name to value) matching the plan.
	// Plan.Vector makes it a similarity search; a store without vectors
	// returns ErrNotTranslatable for it.
	FindDocs(ctx context.Context, table string, p Plan) ([]map[string]any, error)
	// InsertDocs stores documents; the primary key column must be present
	// when the model's key is not client-generated.
	InsertDocs(ctx context.Context, table string, docs []map[string]any) error
	// UpsertDocs stores documents, replacing any existing document that
	// matches on all of keys (upsert).
	UpsertDocs(ctx context.Context, table string, docs []map[string]any, keys []string) error
	// UpdateDocs applies sets to matching documents and reports how many
	// matched.
	UpdateDocs(ctx context.Context, table string, p Plan, sets map[string]any) (int64, error)
	// DeleteDocs removes matching documents and reports how many.
	DeleteDocs(ctx context.Context, table string, p Plan) (int64, error)
	// CountDocs counts matching documents.
	CountDocs(ctx context.Context, table string, p Plan) (int64, error)
}

// AddDocStore registers a document store as a connection. Tables directed
// at it with connection= (or Query.On) run their typed queries against the
// store instead of SQL.
func AddDocStore(name string, ds DocStore) {
	connMu.Lock()
	conns[name] = Conn{Doc: ds}
	connMu.Unlock()
}

func (q Query[M]) docstore(ctx context.Context) (DocStore, bool, error) {
	c, err := lookup(ctx, q.connName())
	if err != nil {
		return nil, false, err
	}
	return c.Doc, c.Doc != nil, nil
}

func docStoreFor(ctx context.Context, conn string) (DocStore, bool, error) {
	c, err := lookup(ctx, conn)
	if err != nil {
		return nil, false, err
	}
	return c.Doc, c.Doc != nil, nil
}

// plan compiles the query for a document store, refusing the parts that
// only exist in SQL.
func (q Query[M]) plan() (Plan, error) {
	p := Plan{Table: q.table.Name, Limit: q.limit, Offset: q.offset}
	notSupported := func(what string) (Plan, error) {
		return Plan{}, &ErrNotTranslatable{Reason: what}
	}
	if len(q.joins) > 0 {
		return notSupported("joins")
	}
	if len(q.groups) > 0 || len(q.having) > 0 {
		return notSupported("groupBy / having")
	}
	if len(q.unions) > 0 {
		return notSupported("unions")
	}
	if q.distinct {
		return notSupported("distinct")
	}
	if len(q.extras) > 0 {
		return notSupported("addSelect aggregates (withCount and friends)")
	}
	if len(q.where) > 0 {
		ir := And(q.where...).ir
		if ir == nil {
			return notSupported("a raw or SQL-specific condition (whereRaw, whereExists, subqueries)")
		}
		if err := checkSameTable(ir, q.table.Name); err != nil {
			return Plan{}, err
		}
		p.Where = ir
	}
	for _, o := range q.orders {
		switch {
		case o.vec != nil:
			if p.Vector != nil {
				return notSupported("more than one vector order")
			}
			p.Vector = o.vec
		case o.col != nil:
			p.Orders = append(p.Orders, PlanOrder{Column: o.col.Name(), Desc: o.desc})
		default:
			return notSupported("orderByRaw or ordering by an expression")
		}
	}
	for _, c := range q.selects {
		p.Columns = append(p.Columns, c.Name())
	}
	return p, nil
}

// planOf is plan with the error wrapped for the calling method's context.
func (q Query[M]) planOf(method string) (Plan, error) {
	p, err := q.plan()
	if err != nil {
		return p, fmt.Errorf("orm: %s on %s: %w", method, q.table.Name, err)
	}
	return p, nil
}

func (q Query[M]) findDocs(ctx context.Context) ([]M, error) {
	ds, ok, err := q.docstore(ctx)
	if err != nil || !ok {
		return nil, err
	}
	p, err := q.planOf("query")
	if err != nil {
		return nil, err
	}
	docs, err := ds.FindDocs(ctx, q.table.Name, p)
	if err != nil {
		return nil, err
	}
	models := make([]M, 0, len(docs))
	for _, doc := range docs {
		m, err := hydrate(q.table, doc)
		if err != nil {
			return nil, err
		}
		q.table.sync(&m)
		models = append(models, m)
	}
	if err := q.finish(ctx, models); err != nil {
		return nil, err
	}
	return models, nil
}

func (q Query[M]) countDocs(ctx context.Context) (int64, bool, error) {
	ds, ok, err := q.docstore(ctx)
	if err != nil || !ok {
		return 0, false, err
	}
	p, err := q.planOf("count")
	if err != nil {
		return 0, true, err
	}
	n, err := ds.CountDocs(ctx, q.table.Name, p)
	return n, true, err
}

func (q Query[M]) docMaps(method string, models []*M) ([]map[string]any, error) {
	t := q.table
	docs := make([]map[string]any, len(models))
	for i, m := range models {
		doc := make(map[string]any, len(t.Columns))
		for _, c := range t.Columns {
			if v := t.dbValue(m, c); v != nil {
				doc[c] = v
			}
		}
		if t.PrimaryKey != "" && t.KeyType == KeyAutoIncrement && isZero(t.key(m)) {
			return nil, fmt.Errorf("orm: %s: %s needs a uuid, ulid or manual key on a document store", method, t.Name)
		}
		docs[i] = doc
	}
	return docs, nil
}

func (q Query[M]) writeDocs(ctx context.Context, method string, models []*M) error {
	ds, ok, err := q.docstore(ctx)
	if err != nil || !ok {
		return err
	}
	docs, err := q.docMaps(method, models)
	if err != nil {
		return err
	}
	return ds.InsertDocs(ctx, q.table.Name, docs)
}

// insertOrIgnoreDocs inserts only the documents whose key combination is
// not present yet. The read-then-write gap means a concurrent writer can
// still insert the same key; a unique index on keys makes that safe.
func insertOrIgnoreDocs(ctx context.Context, ds DocStore, table string, docs []map[string]any, keys []string) error {
	parts := make([]Node, 0, len(keys))
	for _, k := range keys {
		vals := make([]any, 0, len(docs))
		for _, d := range docs {
			if v, ok := d[k]; ok {
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			parts = append(parts, List{Table: table, Column: k, Values: vals})
		}
	}
	existing, err := ds.FindDocs(ctx, table, Plan{Table: table, Where: Composite{Parts: parts}})
	if err != nil {
		return err
	}
	taken := make(map[string]bool, len(existing))
	for _, d := range existing {
		taken[docKey(keys, d)] = true
	}
	fresh := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		if !taken[docKey(keys, d)] {
			fresh = append(fresh, d)
		}
	}
	return ds.InsertDocs(ctx, table, fresh)
}

func docKey(keys []string, doc map[string]any) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprint(doc[k])
	}
	return strings.Join(parts, "\x00")
}

func (q Query[M]) updateDocs(ctx context.Context, sets []Assignment[M], method string) (int64, bool, error) {
	ds, ok, err := q.docstore(ctx)
	if err != nil || !ok {
		return 0, false, err
	}
	p, err := q.planOf(method)
	if err != nil {
		return 0, true, err
	}
	values := make(map[string]any, len(sets))
	for _, a := range sets {
		if a.raw == nil {
			return 0, true, fmt.Errorf("orm: %s on %s: SetExpr assignments need SQL; use Column.Set", method, q.table.Name)
		}
		values[a.col] = a.raw
	}
	n, err := ds.UpdateDocs(ctx, q.table.Name, p, values)
	return n, true, err
}

func (q Query[M]) deleteDocs(ctx context.Context, method string) (int64, bool, error) {
	ds, ok, err := q.docstore(ctx)
	if err != nil || !ok {
		return 0, false, err
	}
	p, err := q.planOf(method)
	if err != nil {
		return 0, true, err
	}
	n, err := ds.DeleteDocs(ctx, q.table.Name, p)
	return n, true, err
}

// checkSameTable rejects conditions on other tables' columns (joins),
// which document stores cannot evaluate.
func checkSameTable(n Node, table string) error {
	foreign := func(t string) bool { return t != "" && t != table }
	switch c := n.(type) {
	case Field:
		if foreign(c.Table) {
			return &ErrNotTranslatable{Reason: "conditions on a column of " + c.Table}
		}
	case List:
		if foreign(c.Table) {
			return &ErrNotTranslatable{Reason: "conditions on a column of " + c.Table}
		}
	case Range:
		if foreign(c.Table) {
			return &ErrNotTranslatable{Reason: "conditions on a column of " + c.Table}
		}
	case NullTest:
		if foreign(c.Table) {
			return &ErrNotTranslatable{Reason: "conditions on a column of " + c.Table}
		}
	case Composite:
		for _, part := range c.Parts {
			if err := checkSameTable(part, table); err != nil {
				return err
			}
		}
	case Negate:
		return checkSameTable(c.Part, table)
	case notTranslatable:
		return &ErrNotTranslatable{Reason: c.reason}
	}
	return nil
}

// hydrate fills a model from a document's column values, reusing the
// driver-value conversion SQL scanning uses.
func hydrate[M any](t *Table[M], doc map[string]any) (M, error) {
	var m M
	for _, c := range t.Columns {
		v, ok := doc[c]
		if !ok {
			continue
		}
		if err := (nullable{t.Ptr(&m, c)}).Scan(v); err != nil {
			return m, fmt.Errorf("orm: hydrating %s.%s: %w", t.Name, c, err)
		}
	}
	return m, nil
}
