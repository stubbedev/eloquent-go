package orm

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
)

// ErrStop can be returned from a Chunk callback to stop without an error.
var ErrStop = errors.New("orm: stop iteration")

// Get runs the query and returns all matching models with eager loads applied.
func (q Query[M]) Get(ctx context.Context) ([]M, error) {
	q = q.prepared()
	rows, err := q.query(ctx, func(b *SQL) { q.renderSelect(b, nil) })
	if err != nil {
		return nil, err
	}
	models, err := q.scan(rows)
	if err != nil {
		return nil, err
	}
	if err := q.finish(ctx, models); err != nil {
		return nil, err
	}
	return models, nil
}

func (q Query[M]) scan(rows *sql.Rows) ([]M, error) {
	defer rows.Close()
	dests := q.dests()
	var models []M
	for rows.Next() {
		var m M
		ptrs := make([]any, len(dests))
		for i, d := range dests {
			p := q.table.Ptr(&m, d)
			if p == nil {
				return nil, fmt.Errorf("orm: %s has no field for selected column %q", q.table.Name, d)
			}
			ptrs[i] = nullable{p}
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		q.table.sync(&m)
		models = append(models, m)
	}
	return models, rows.Err()
}

// finish runs eager loads and Retrieved events.
func (q Query[M]) finish(ctx context.Context, models []M) error {
	for _, load := range q.eager {
		if err := load(ctx, q.onConn, models); err != nil {
			return err
		}
	}
	for i := range models {
		if err := q.table.fire(ctx, Retrieved, &models[i]); err != nil {
			return err
		}
	}
	return nil
}

// All returns every row (Model::all()).
func (t *Table[M]) All(ctx context.Context) ([]M, error) { return t.Query().Get(ctx) }

// First returns the first matching model or ErrNotFound (first / firstOrFail).
func (q Query[M]) First(ctx context.Context) (M, error) {
	models, err := q.Limit(1).Get(ctx)
	if err != nil || len(models) == 0 {
		var zero M
		return zero, cmp.Or(err, ErrNotFound)
	}
	return models[0], nil
}

// FirstOr returns the first match, or the result of fn if there is none.
func (q Query[M]) FirstOr(ctx context.Context, fn func() (M, error)) (M, error) {
	m, err := q.First(ctx)
	if errors.Is(err, ErrNotFound) {
		return fn()
	}
	return m, err
}

// Sole returns the only matching model; ErrNotFound or ErrMultipleRecords otherwise.
func (q Query[M]) Sole(ctx context.Context) (M, error) {
	models, err := q.Limit(2).Get(ctx)
	var zero M
	switch {
	case err != nil:
		return zero, err
	case len(models) == 0:
		return zero, ErrNotFound
	case len(models) > 1:
		return zero, ErrMultipleRecords
	}
	return models[0], nil
}

// Find returns the model with the given primary key (find / findOrFail).
func (q Query[M]) Find(ctx context.Context, id any) (M, error) { return q.WhereKey(id).First(ctx) }

// FindMany returns the models with the given primary keys.
func (q Query[M]) FindMany(ctx context.Context, ids ...any) ([]M, error) {
	return q.WhereKey(ids...).Get(ctx)
}

func (t *Table[M]) Find(ctx context.Context, id any) (M, error) { return t.Query().Find(ctx, id) }

func (t *Table[M]) Where(conds ...Cond[M]) Query[M] { return t.Query().Where(conds...) }

// ---------------------------------------------------------------------------
// Aggregates and single values
// ---------------------------------------------------------------------------

func (q Query[M]) Exists(ctx context.Context) (bool, error) {
	q = q.prepared()
	q.extras, q.eager = nil, nil
	rows, err := q.Limit(1).query(ctx, func(b *SQL) { q.Limit(1).renderSelect(b, constSelect("1")) })
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func (q Query[M]) DoesntExist(ctx context.Context) (bool, error) {
	ok, err := q.Exists(ctx)
	return !ok, err
}

func (q Query[M]) Count(ctx context.Context) (int64, error) {
	q = q.prepared()
	if len(q.groups) > 0 || q.distinct || len(q.unions) > 0 {
		inner := q.forAggregate()
		return scalarOf[int64](ctx, q, func(b *SQL) {
			b.Write("SELECT COUNT(*) FROM (")
			inner.renderSelect(b, nil)
			b.Write(") AS ")
			b.Ident("aggregate")
		})
	}
	return aggregate[int64](ctx, q, Count[M]())
}

// Sum only accepts numeric expressions: Sum(ctx, Users.Email) does not compile.
func (q Query[M]) Sum[V Number](ctx context.Context, e Expr[M, V]) (V, error) {
	return aggregate[V](ctx, q, Sum(e))
}

func (q Query[M]) Avg[V Number](ctx context.Context, e Expr[M, V]) (float64, error) {
	return aggregate[float64](ctx, q, Avg(e))
}

func (q Query[M]) Min[V any](ctx context.Context, e Expr[M, V]) (V, error) {
	return aggregate[V](ctx, q, Min(e))
}

func (q Query[M]) Max[V any](ctx context.Context, e Expr[M, V]) (V, error) {
	return aggregate[V](ctx, q, Max(e))
}

// Pluck returns one expression per row; V comes from the expression, so
// Pluck(ctx, Users.Email) is a []string.
func (q Query[M]) Pluck[V any](ctx context.Context, e Expr[M, V]) ([]V, error) {
	q = q.prepared()
	rows, err := q.query(ctx, func(b *SQL) { q.renderSelect(b, e.build) })
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V
	for rows.Next() {
		var v V
		if err := rows.Scan(nullable{&v}); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PluckMap is pluck('value', 'key').
func (q Query[M]) PluckMap[K comparable, V any](ctx context.Context, key Expr[M, K], val Expr[M, V]) (map[K]V, error) {
	q = q.prepared()
	rows, err := q.query(ctx, func(b *SQL) {
		q.renderSelect(b, func(b *SQL) { val.build(b); b.Write(", "); key.build(b) })
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[K]V{}
	for rows.Next() {
		var k K
		var v V
		if err := rows.Scan(nullable{&v}, nullable{&k}); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Value returns one expression from the first row.
func (q Query[M]) Value[V any](ctx context.Context, e Expr[M, V]) (V, error) {
	vs, err := q.Limit(1).Pluck(ctx, e)
	if err != nil || len(vs) == 0 {
		var zero V
		return zero, cmp.Or(err, ErrNotFound)
	}
	return vs[0], nil
}

// KeyBy returns the models indexed by a column.
func (q Query[M]) KeyBy[K comparable](ctx context.Context, col Column[M, K]) (map[K]M, error) {
	models, err := q.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[K]M, len(models))
	for i := range models {
		out[col.Get(&models[i])] = models[i]
	}
	return out, nil
}

// Grouped returns the models grouped by a column (collection groupBy; the
// name GroupBy is the SQL clause).
func (q Query[M]) Grouped[K comparable](ctx context.Context, col Column[M, K]) (map[K][]M, error) {
	models, err := q.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := map[K][]M{}
	for i := range models {
		k := col.Get(&models[i])
		out[k] = append(out[k], models[i])
	}
	return out, nil
}

// Map runs the query and transforms each model.
func (q Query[M]) Map[R any](ctx context.Context, fn func(M) R) ([]R, error) {
	models, err := q.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]R, len(models))
	for i, m := range models {
		out[i] = fn(m)
	}
	return out, nil
}

func (q Query[M]) forAggregate() Query[M] {
	q.orders, q.limit, q.offset, q.eager, q.extras = nil, 0, 0, nil, nil
	return q
}

func aggregate[V, M any](ctx context.Context, q Query[M], e AnyExpr) (V, error) {
	q = q.prepared().forAggregate()
	return scalarOf[V](ctx, q, func(b *SQL) { q.renderSelect(b, e.build) })
}

func scalarOf[V, M any](ctx context.Context, q Query[M], f frag) (V, error) {
	var v V
	rows, err := q.query(ctx, f)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(nullable{&v}); err != nil {
			return v, err
		}
	}
	return v, rows.Err()
}

// ---------------------------------------------------------------------------
// Chunking and iteration
// ---------------------------------------------------------------------------

// Chunk processes results in pages of size (ordered queries only).
// Return ErrStop from fn to stop early.
func (q Query[M]) Chunk(ctx context.Context, size int, fn func([]M) error) error {
	for page := 1; ; page++ {
		models, err := q.ForPage(page, size).Get(ctx)
		if err != nil {
			return err
		}
		if len(models) > 0 {
			if err := fn(models); err != nil {
				return ignoreStop(err)
			}
		}
		if len(models) < size {
			return nil
		}
	}
}

// ChunkByID pages by primary key, safe when fn modifies the rows it gets.
func (q Query[M]) ChunkByID(ctx context.Context, size int, fn func([]M) error) error {
	for models, err := range q.chunksByID(ctx, size) {
		if err != nil {
			return err
		}
		if err := fn(models); err != nil {
			return ignoreStop(err)
		}
	}
	return nil
}

// Lazy streams models chunk by chunk (lazyById), with eager loads per chunk:
//
//	for u, err := range Users.Query().With(UserPosts).Lazy(ctx, 500) { ... }
func (q Query[M]) Lazy(ctx context.Context, size int) iter.Seq2[M, error] {
	return func(yield func(M, error) bool) {
		for models, err := range q.chunksByID(ctx, size) {
			if err != nil {
				var zero M
				yield(zero, err)
				return
			}
			for _, m := range models {
				if !yield(m, nil) {
					return
				}
			}
		}
	}
}

func (q Query[M]) chunksByID(ctx context.Context, size int) iter.Seq2[[]M, error] {
	return func(yield func([]M, error) bool) {
		var last any
		for {
			page := q.Reorder(Order[M]{f: func(b *SQL) { q.keyCol()(b); b.Write(" ASC") }}).Limit(size)
			if last != nil {
				page = page.Where(Scalar[M, any]{q.keyCol()}.Gt(last))
			}
			models, err := page.Get(ctx)
			if err != nil {
				yield(nil, err)
				return
			}
			if len(models) == 0 || !yield(models, nil) || len(models) < size {
				return
			}
			last = q.table.key(&models[len(models)-1])
		}
	}
}

// Cursor streams rows one at a time over a single query, without eager
// loading (cursor()).
func (q Query[M]) Cursor(ctx context.Context) iter.Seq2[M, error] {
	return func(yield func(M, error) bool) {
		var zero M
		q := q.prepared()
		rows, err := q.query(ctx, func(b *SQL) { q.renderSelect(b, nil) })
		if err != nil {
			yield(zero, err)
			return
		}
		defer rows.Close()
		dests := q.dests()
		for rows.Next() {
			var m M
			ptrs := make([]any, len(dests))
			for i, d := range dests {
				ptrs[i] = nullable{q.table.Ptr(&m, d)}
			}
			if err := rows.Scan(ptrs...); err != nil {
				yield(zero, err)
				return
			}
			q.table.sync(&m)
			if err := q.table.fire(ctx, Retrieved, &m); err != nil {
				yield(zero, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, err)
		}
	}
}

func ignoreStop(err error) error {
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

// Page is a length-aware page (paginate()).
type Page[M any] struct {
	Items       []M   `json:"data"`
	Total       int64 `json:"total"`
	PerPage     int   `json:"per_page"`
	CurrentPage int   `json:"current_page"`
	LastPage    int   `json:"last_page"`
}

// SimplePage is a page without a total count (simplePaginate()).
type SimplePage[M any] struct {
	Items       []M  `json:"data"`
	PerPage     int  `json:"per_page"`
	CurrentPage int  `json:"current_page"`
	HasMore     bool `json:"has_more"`
}

// CursorPage is a keyset page (cursorPaginate()).
type CursorPage[M any] struct {
	Items      []M    `json:"data"`
	PerPage    int    `json:"per_page"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (q Query[M]) Paginate(ctx context.Context, perPage, page int) (Page[M], error) {
	total, err := q.Count(ctx)
	if err != nil {
		return Page[M]{}, err
	}
	items, err := q.ForPage(page, perPage).Get(ctx)
	return Page[M]{
		Items: items, Total: total, PerPage: perPage, CurrentPage: page,
		LastPage: max(1, int((total+int64(perPage)-1)/int64(perPage))),
	}, err
}

func (q Query[M]) SimplePaginate(ctx context.Context, perPage, page int) (SimplePage[M], error) {
	items, err := q.Offset(max(page-1, 0) * perPage).Limit(perPage + 1).Get(ctx)
	more := len(items) > perPage
	if more {
		items = items[:perPage]
	}
	return SimplePage[M]{Items: items, PerPage: perPage, CurrentPage: page, HasMore: more}, err
}

// CursorPaginate pages by the values of the ORDER BY columns (keyset
// pagination). Orders must be plain columns; with none, the primary key is used.
func (q Query[M]) CursorPaginate(ctx context.Context, perPage int, cursor string) (CursorPage[M], error) {
	orders := q.orders
	if len(orders) == 0 {
		pk := tableColumn[M]{q.table, q.table.PrimaryKey}
		orders = []Order[M]{{f: func(b *SQL) { pk.build(b); b.Write(" ASC") }, col: pk}}
		q = q.Reorder(orders...)
	}
	for _, o := range orders {
		if o.col == nil {
			return CursorPage[M]{}, errors.New("orm: CursorPaginate needs column orders")
		}
	}
	if cursor != "" {
		cond, err := cursorCond(orders, cursor)
		if err != nil {
			return CursorPage[M]{}, err
		}
		q = q.Where(cond)
	}
	items, err := q.Limit(perPage + 1).Get(ctx)
	if err != nil {
		return CursorPage[M]{}, err
	}
	page := CursorPage[M]{Items: items, PerPage: perPage}
	if len(items) > perPage {
		page.Items = items[:perPage]
		last := &items[perPage-1]
		vals := make([]any, len(orders))
		for i, o := range orders {
			vals[i] = o.col.anyValue(last)
		}
		raw, err := json.Marshal(vals)
		if err != nil {
			return page, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

// cursorCond builds (a > x) OR (a = x AND b > y) ... respecting directions.
func cursorCond[M any](orders []Order[M], cursor string) (Cond[M], error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return Cond[M]{}, fmt.Errorf("orm: bad cursor: %w", err)
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != len(orders) {
		return Cond[M]{}, fmt.Errorf("orm: bad cursor")
	}
	vals := make([]any, len(parts))
	for i, p := range parts {
		if vals[i], err = orders[i].col.decode(p); err != nil {
			return Cond[M]{}, fmt.Errorf("orm: bad cursor: %w", err)
		}
	}
	var ors []Cond[M]
	for i := range orders {
		var ands []Cond[M]
		for j := 0; j <= i; j++ {
			op := "="
			if j == i {
				op = map[bool]string{false: ">", true: "<"}[orders[j].desc]
			}
			col, v := orders[j].col, vals[j]
			ands = append(ands, Cond[M]{func(b *SQL) { col.build(b); b.Write(" ", op, " "); b.Arg(v) }})
		}
		ors = append(ors, And(ands...))
	}
	return Or(ors...), nil
}

func decodeInto(raw json.RawMessage, ptr any) (any, error) {
	v := reflect.New(reflect.TypeOf(ptr).Elem())
	if err := json.Unmarshal(raw, v.Interface()); err != nil {
		return nil, err
	}
	return v.Elem().Interface(), nil
}
