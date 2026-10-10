package orm

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// From starts a query on a table without a model — Eloquent's
// DB::table('users'). Rows come back as maps; values are bound as
// arguments and never interpolated. Model-level features (events, casts,
// relations) do not apply.
//
//	rows, err := orm.From("users").
//		Where("active", "=", true).
//		WhereIn("country_id", 1, 2).
//		WhereNull("deleted_at").
//		OrderBy("name").
//		Get(ctx) // []map[string]any{{"id": int64(1), "name": "Alice"}, ...}
//
// Like Query, every method returns a new RowQuery, so builders can be shared.
type RowQuery struct {
	conn     string
	name     string
	key      string // auto-increment key column for InsertGetID; default "id"
	distinct bool
	selects  []string
	rawSel   string
	joins    []frag
	where    []func(*SQL)
	orders   []frag
	limit    int
	offset   int
}

// FromTable is From for a table on a named connection.
func From(name string) RowQuery { return RowQuery{name: name, key: "id"} }

// On targets a named connection (DB::table(...)->on('analytics')).
func (r RowQuery) On(conn string) RowQuery { r.conn = conn; return r }

// WithKey names the auto-increment column InsertGetID returns (Postgres
// needs it for RETURNING; on SQLite "rowid" works for keyless tables).
func (r RowQuery) WithKey(col string) RowQuery { r.key = col; return r }

var comparisonOps = map[string]bool{
	"=": true, "<>": true, "!=": true, ">": true, "<": true, ">=": true, "<=": true,
	"LIKE": true, "NOT LIKE": true, "ILIKE": true, "NOT ILIKE": true,
}

func (r RowQuery) cond(col, op string, v any) func(*SQL) {
	if !comparisonOps[op] {
		panic(fmt.Sprintf("orm: unknown comparison operator %q", op))
	}
	return func(b *SQL) {
		b.Ident(col)
		b.Write(" ", op, " ")
		b.Arg(v)
	}
}

// Where adds a simple column condition (where('active', '=', true)).
func (r RowQuery) Where(col, op string, v any) RowQuery {
	r.where = append(slices.Clip(r.where), r.cond(col, op, v))
	return r
}

// OrWhere ORs the condition with everything so far.
func (r RowQuery) OrWhere(col, op string, v any) RowQuery {
	if len(r.where) == 0 {
		return r.Where(col, op, v)
	}
	first, second := r.where, []func(*SQL){r.cond(col, op, v)}
	r.where = []func(*SQL){func(b *SQL) {
		b.Write("(")
		for i, f := range first {
			if i > 0 {
				b.Write(" AND ")
			}
			f(b)
		}
		b.Write(") OR (")
		second[0](b)
		b.Write(")")
	}}
	return r
}

// WhereIn is whereIn on a raw column.
func (r RowQuery) WhereIn(col string, vs ...any) RowQuery {
	return r.whereIn("IN", col, vs)
}

func (r RowQuery) WhereNotIn(col string, vs ...any) RowQuery {
	return r.whereIn("NOT IN", col, vs)
}

func (r RowQuery) whereIn(op, col string, vs []any) RowQuery {
	return r.whereRawCol(func(b *SQL) {
		b.Ident(col)
		b.Write(" ", op, " (")
		if len(vs) == 0 {
			b.Write(map[string]string{"IN": "1 = 0", "NOT IN": "1 = 1"}[op])
		}
		for i, v := range vs {
			if i > 0 {
				b.Write(", ")
			}
			b.Arg(v)
		}
		b.Write(")")
	})
}

// WhereNull and WhereNotNull filter on column nullness.
func (r RowQuery) WhereNull(col string) RowQuery {
	return r.whereRawCol(func(b *SQL) { b.Ident(col); b.Write(" IS NULL") })
}

func (r RowQuery) WhereNotNull(col string) RowQuery {
	return r.whereRawCol(func(b *SQL) { b.Ident(col); b.Write(" IS NOT NULL") })
}

// WhereBetween filters a column between lo and hi.
func (r RowQuery) WhereBetween(col string, lo, hi any) RowQuery {
	return r.whereRawCol(func(b *SQL) {
		b.Ident(col)
		b.Write(" BETWEEN ")
		b.Arg(lo)
		b.Write(" AND ")
		b.Arg(hi)
	})
}

// WhereRaw adds a condition from SQL with ? placeholders (whereRaw).
func (r RowQuery) WhereRaw(sqlFragment string, args ...any) RowQuery {
	return r.whereRawCol(func(b *SQL) { b.Raw(sqlFragment, args...) })
}

func (r RowQuery) whereRawCol(f func(*SQL)) RowQuery {
	r.where = append(slices.Clip(r.where), f)
	return r
}

// Select limits the selected columns; default is *.
func (r RowQuery) Select(cols ...string) RowQuery {
	r.selects = slices.Clone(cols)
	return r
}

func (r RowQuery) Distinct() RowQuery { r.distinct = true; return r }

// Join adds a join on a column pair: Join("countries", "users.country_id", "=", "countries.id").
func (r RowQuery) Join(table, first, op, second string) RowQuery {
	return r.join("JOIN", table, first, op, second)
}

func (r RowQuery) LeftJoin(table, first, op, second string) RowQuery {
	return r.join("LEFT JOIN", table, first, op, second)
}

func (r RowQuery) join(kind, table, first, op, second string) RowQuery {
	if !comparisonOps[op] {
		panic(fmt.Sprintf("orm: unknown comparison operator %q", op))
	}
	r.joins = append(slices.Clip(r.joins), func(b *SQL) {
		b.Write(" ", kind, " ")
		b.Ident(table)
		b.Write(" ON ")
		b.Ident(first)
		b.Write(" ", op, " ")
		b.Ident(second)
	})
	return r
}

// OrderBy sorts ascending; OrderByDesc descending.
func (r RowQuery) OrderBy(col string) RowQuery {
	return r.order(col, " ASC")
}

func (r RowQuery) OrderByDesc(col string) RowQuery {
	return r.order(col, " DESC")
}

func (r RowQuery) order(col, dir string) RowQuery {
	r.orders = append(slices.Clip(r.orders), func(b *SQL) { b.Ident(col); b.Write(dir) })
	return r
}

func (r RowQuery) Limit(n int) RowQuery  { r.limit = n; return r }
func (r RowQuery) Offset(n int) RowQuery { r.offset = n; return r }

func (r RowQuery) render(b *SQL, f frag) {
	b.Write("SELECT ")
	if r.distinct {
		b.Write("DISTINCT ")
	}
	switch {
	case r.rawSel != "":
		b.Write(r.rawSel)
	case len(r.selects) == 0:
		b.Write("*")
	default:
		for i, c := range r.selects {
			if i > 0 {
				b.Write(", ")
			}
			b.Ident(c)
		}
	}
	b.Write(" FROM ")
	b.Ident(r.name)
	for _, j := range r.joins {
		j(b)
	}
	if len(r.where) > 0 {
		b.Write(" WHERE ")
		for i, w := range r.where {
			if i > 0 {
				b.Write(" AND ")
			}
			w(b)
		}
	}
	if len(r.orders) > 0 {
		b.Write(" ORDER BY ")
		for i, o := range r.orders {
			if i > 0 {
				b.Write(", ")
			}
			o(b)
		}
	}
	b.Dialect.Limit(b, r.limit, r.offset)
	if f != nil {
		f(b)
	}
}

func (r RowQuery) conn1(ctx context.Context) (Conn, error) {
	return lookup(ctx, cmp.Or(r.conn, DefaultConnection))
}

func (r RowQuery) run(ctx context.Context, f frag) (*sql.Rows, error) {
	c, err := r.conn1(ctx)
	if err != nil {
		return nil, err
	}
	stmt, args := render(c.Dialect, func(b *SQL) { r.render(b, f) })
	start := time.Now()
	rows, err := stickyDB(ctx, cmp.Or(r.conn, DefaultConnection), c).QueryContext(ctx, stmt, args...)
	Emit(QueryEvent{Connection: cmp.Or(r.conn, DefaultConnection), SQL: stmt, Args: args, Duration: time.Since(start), Err: err})
	if err != nil {
		return nil, &QueryError{Err: err, SQL: stmt}
	}
	return rows, nil
}

func (r RowQuery) exec(ctx context.Context, f func(*SQL)) (sql.Result, error) {
	c, err := r.conn1(ctx)
	if err != nil {
		return nil, err
	}
	stmt, args := render(c.Dialect, f)
	start := time.Now()
	res, err := c.DB.ExecContext(ctx, stmt, args...)
	Emit(QueryEvent{Connection: cmp.Or(r.conn, DefaultConnection), SQL: stmt, Args: args, Duration: time.Since(start), Err: err})
	if err != nil {
		return nil, &QueryError{Err: err, SQL: stmt}
	}
	markWritten(ctx, cmp.Or(r.conn, DefaultConnection))
	return res, nil
}

// Get runs the query and returns every row as a map of column to value.
func (r RowQuery) Get(ctx context.Context) ([]map[string]any, error) {
	rows, err := r.run(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMaps(rows)
}

// First returns the first row or ErrNotFound.
func (r RowQuery) First(ctx context.Context) (map[string]any, error) {
	rows, err := r.Limit(1).Get(ctx)
	if err != nil || len(rows) == 0 {
		return nil, cmp.Or(err, ErrNotFound)
	}
	return rows[0], nil
}

// Pluck returns one column per row.
func (r RowQuery) Pluck(ctx context.Context, col string) ([]any, error) {
	rows, err := r.Select(col).Get(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		out[i] = row[col]
	}
	return out, nil
}

// Count returns the number of matching rows.
func (r RowQuery) Count(ctx context.Context) (int64, error) {
	r.selects, r.distinct, r.rawSel, r.orders, r.limit, r.offset = nil, false, "COUNT(*)", nil, 0, 0
	var n int64
	rows, err := r.run(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// Exists reports whether any row matches.
func (r RowQuery) Exists(ctx context.Context) (bool, error) {
	r.selects, r.rawSel = nil, "1"
	rows, err := r.Limit(1).run(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// Insert inserts the given rows as-is.
func (r RowQuery) Insert(ctx context.Context, ms ...map[string]any) error {
	_, err := r.insert(ctx, ms)
	return err
}

// InsertGetID inserts one row and returns the database-generated key,
// from the key column ("id" unless changed with WithKey).
func (r RowQuery) InsertGetID(ctx context.Context, m map[string]any) (int64, error) {
	c, err := r.conn1(ctx)
	if err != nil {
		return 0, err
	}
	if c.Dialect.Returning() {
		rows, err := r.insertReturning(ctx, m)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		if rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return 0, err
			}
			return id, rows.Err()
		}
		return 0, rows.Err()
	}
	res, err := r.insert(ctx, []map[string]any{m})
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r RowQuery) insertReturning(ctx context.Context, m map[string]any) (*sql.Rows, error) {
	c, err := r.conn1(ctx)
	if err != nil {
		return nil, err
	}
	keyCol := r.key
	stmt, args := render(c.Dialect, func(b *SQL) {
		r.writeInsert(b, []map[string]any{m})
		b.Write(" RETURNING ")
		b.Ident(keyCol)
	})
	start := time.Now()
	rows, err := c.DB.QueryContext(ctx, stmt, args...)
	Emit(QueryEvent{Connection: cmp.Or(r.conn, DefaultConnection), SQL: stmt, Args: args, Duration: time.Since(start), Err: err})
	if err != nil {
		return nil, &QueryError{Err: err, SQL: stmt}
	}
	markWritten(ctx, cmp.Or(r.conn, DefaultConnection))
	return rows, nil
}

func (r RowQuery) insert(ctx context.Context, ms []map[string]any) (sql.Result, error) {
	if len(ms) == 0 {
		return nil, nil
	}
	return r.exec(ctx, func(b *SQL) { r.writeInsert(b, ms) })
}

func (r RowQuery) writeInsert(b *SQL, ms []map[string]any) {
	cols := map[string]bool{}
	for _, m := range ms {
		for c := range m {
			cols[c] = true
		}
	}
	names := sortedKeys(cols)
	b.Write("INSERT INTO ")
	b.Ident(r.name)
	b.Write(" (")
	for i, c := range names {
		if i > 0 {
			b.Write(", ")
		}
		b.Ident(c)
	}
	b.Write(") VALUES ")
	for i, m := range ms {
		if i > 0 {
			b.Write(", ")
		}
		b.Write("(")
		for j, c := range names {
			if j > 0 {
				b.Write(", ")
			}
			b.Arg(m[c])
		}
		b.Write(")")
	}
}

// sortedKeys returns the map's keys in order, so generated SQL is stable.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// Update sets the given values on every matching row and reports how many
// changed.
func (r RowQuery) Update(ctx context.Context, m map[string]any) (int64, error) {
	res, err := r.exec(ctx, func(b *SQL) {
		b.Write("UPDATE ")
		b.Ident(r.name)
		b.Write(" SET ")
		for i, c := range sortedKeys(m) {
			if i > 0 {
				b.Write(", ")
			}
			b.Ident(c)
			b.Write(" = ")
			b.Arg(m[c])
		}
		if len(r.where) > 0 {
			b.Write(" WHERE ")
			for i, w := range r.where {
				if i > 0 {
					b.Write(" AND ")
				}
				w(b)
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateOrInsert updates the first row matching attrs with values, or
// inserts attrs and values when none matches.
func (r RowQuery) UpdateOrInsert(ctx context.Context, attrs, values map[string]any) error {
	base := r
	base.where = nil
	for _, c := range sortedKeys(attrs) {
		base = base.Where(c, "=", attrs[c])
	}
	n, err := base.Update(ctx, values)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	merged := map[string]any{}
	for _, c := range sortedKeys(attrs) {
		merged[c] = attrs[c]
	}
	for _, c := range sortedKeys(values) {
		merged[c] = values[c]
	}
	return base.Insert(ctx, merged)
}

// Delete removes every matching row and reports how many went.
func (r RowQuery) Delete(ctx context.Context) (int64, error) {
	res, err := r.exec(ctx, func(b *SQL) {
		b.Write("DELETE FROM ")
		b.Ident(r.name)
		if len(r.where) > 0 {
			b.Write(" WHERE ")
			for i, w := range r.where {
				if i > 0 {
					b.Write(" AND ")
				}
				w(b)
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanMaps reads every remaining row into maps, with []byte values
// converted to strings.
func scanMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
