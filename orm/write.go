package orm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
)

// now is the timestamp used for created/updated/deleted columns, at second
// precision like Laravel's default date format. Replaceable in tests.
var now = func() time.Time { return time.Now().UTC().Truncate(time.Second) }

// ---------------------------------------------------------------------------
// Single-model writes (fire events, maintain timestamps and dirty state)
// ---------------------------------------------------------------------------

func (t *Table[M]) Create(ctx context.Context, m *M) error { return t.Query().Create(ctx, m) }
func (t *Table[M]) Save(ctx context.Context, m *M) error   { return t.Query().Save(ctx, m) }
func (t *Table[M]) Delete(ctx context.Context, m *M) error { return t.Query().DeleteModel(ctx, m) }

// Create inserts m. Auto-increment keys are read back, UUID keys generated.
func (q Query[M]) Create(ctx context.Context, m *M) error {
	if q.table.state(m).exists {
		return errors.New("orm: Create called on a model that already exists; use Save")
	}
	return q.Save(ctx, m)
}

// Save inserts m if it is new, otherwise updates its dirty columns.
func (q Query[M]) Save(ctx context.Context, m *M) error {
	t := q.table
	if err := t.fire(ctx, Saving, m); err != nil {
		return err
	}
	var err error
	if t.state(m).exists {
		err = q.performUpdate(ctx, m)
	} else {
		err = q.performInsert(ctx, m)
	}
	if err != nil {
		return err
	}
	return t.fire(ctx, Saved, m)
}

func (q Query[M]) performInsert(ctx context.Context, m *M) error {
	t := q.table
	if err := t.fire(ctx, Creating, m); err != nil {
		return err
	}
	if timestamps(ctx) {
		ts := now()
		for _, c := range []string{t.CreatedAt, t.UpdatedAt} {
			if c != "" && isZero(t.value(m, c)) {
				t.setValue(m, c, ts)
			}
		}
	}
	switch {
	case t.KeyType == KeyUUID && isZero(t.key(m)):
		t.setValue(m, t.PrimaryKey, NewUUIDv7())
	case t.KeyType == KeyULID && isZero(t.key(m)):
		t.setValue(m, t.PrimaryKey, NewULID())
	}
	autoKey := t.KeyType == KeyAutoIncrement && t.PrimaryKey != "" && isZero(t.key(m))
	cols := slices.DeleteFunc(slices.Clone(t.Columns), func(c string) bool { return autoKey && c == t.PrimaryKey })

	c, err := q.conn(ctx)
	if err != nil {
		return err
	}
	stmt := func(b *SQL) {
		q.renderInsert(b, "INSERT", cols, []*M{m})
		if autoKey && c.Dialect.Returning() {
			b.Write(" RETURNING ")
			b.Ident(t.PrimaryKey)
		}
	}
	switch {
	case autoKey && c.Dialect.Returning():
		rows, err := q.query(ctx, stmt)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(t.Ptr(m, t.PrimaryKey)); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	default:
		res, err := q.exec(ctx, stmt)
		if err != nil {
			return err
		}
		if autoKey {
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			t.setValue(m, t.PrimaryKey, id)
		}
	}
	st := t.state(m)
	t.sync(m)
	st.recentlyCreated = true
	st.changes = slices.Clone(t.Columns)
	return t.fire(ctx, Created, m)
}

func (q Query[M]) performUpdate(ctx context.Context, m *M) error {
	t := q.table
	st := t.state(m)
	if len(t.Dirty(m)) == 0 {
		st.changes = nil
		return nil
	}
	if err := t.fire(ctx, Updating, m); err != nil {
		return err
	}
	if t.UpdatedAt != "" && timestamps(ctx) && !slices.Contains(t.Dirty(m), t.UpdatedAt) {
		t.setValue(m, t.UpdatedAt, now())
	}
	dirty := t.Dirty(m)
	sets := make([]Assignment[M], len(dirty))
	for i, c := range dirty {
		v := t.dbValue(m, c)
		sets[i] = Assignment[M]{col: c, val: func(b *SQL) { b.Arg(v) }}
	}
	if _, err := q.instance(m).update(ctx, sets); err != nil {
		return err
	}
	t.sync(m)
	st.recentlyCreated = false
	st.changes = dirty
	return t.fire(ctx, Updated, m)
}

// instance is a query for exactly m's row, by its original key, ignoring
// global scopes (newModelQuery()).
func (q Query[M]) instance(m *M) Query[M] {
	t := q.table
	key, ok := t.state(m).original[t.PrimaryKey]
	if !ok {
		key = t.key(m)
	}
	return t.Query().On(q.onConn).WithoutGlobalScopes().WhereKey(key)
}

// DeleteModel deletes m — soft deletes it if the table uses soft deletes.
func (q Query[M]) DeleteModel(ctx context.Context, m *M) error {
	t := q.table
	if t.PrimaryKey == "" {
		return errors.New("orm: cannot delete a model without a primary key")
	}
	if err := t.fire(ctx, Deleting, m); err != nil {
		return err
	}
	if t.softDeletes() {
		ts := now()
		t.setValue(m, t.DeletedAt, ts)
		sets := []Assignment[M]{{col: t.DeletedAt, val: func(b *SQL) { b.Arg(ts) }}}
		if t.UpdatedAt != "" && timestamps(ctx) {
			t.setValue(m, t.UpdatedAt, ts)
			sets = append(sets, Assignment[M]{col: t.UpdatedAt, val: func(b *SQL) { b.Arg(ts) }})
		}
		if _, err := q.instance(m).update(ctx, sets); err != nil {
			return err
		}
		t.sync(m)
		if err := t.fire(ctx, Trashed, m); err != nil {
			return err
		}
	} else {
		if _, err := q.instance(m).hardDelete(ctx); err != nil {
			return err
		}
		t.state(m).exists = false
	}
	return t.fire(ctx, Deleted, m)
}

// ForceDelete permanently deletes m, even with soft deletes.
func (q Query[M]) ForceDeleteModel(ctx context.Context, m *M) error {
	t := q.table
	if err := t.fire(ctx, ForceDeleting, m); err != nil {
		return err
	}
	if err := t.fire(ctx, Deleting, m); err != nil {
		return err
	}
	if _, err := q.instance(m).hardDelete(ctx); err != nil {
		return err
	}
	t.state(m).exists = false
	if err := t.fire(ctx, Deleted, m); err != nil {
		return err
	}
	return t.fire(ctx, ForceDeleted, m)
}

func (t *Table[M]) ForceDelete(ctx context.Context, m *M) error {
	return t.Query().ForceDeleteModel(ctx, m)
}

// Restore un-deletes a soft deleted model.
func (t *Table[M]) Restore(ctx context.Context, m *M) error {
	if err := t.fire(ctx, Restoring, m); err != nil {
		return err
	}
	reflect.ValueOf(t.Ptr(m, t.DeletedAt)).Elem().SetZero()
	if err := t.Query().Save(ctx, m); err != nil {
		return err
	}
	return t.fire(ctx, Restored, m)
}

// Trashed reports whether m is soft deleted.
func (t *Table[M]) Trashed(m *M) bool { return t.softDeletes() && !isZero(t.value(m, t.DeletedAt)) }

// Touch updates m's updated-at timestamp.
func (t *Table[M]) Touch(ctx context.Context, m *M) error {
	if t.UpdatedAt == "" {
		return nil
	}
	t.setValue(m, t.UpdatedAt, now())
	return t.Save(ctx, m)
}

// Refresh reloads m's columns from the database.
func (t *Table[M]) Refresh(ctx context.Context, m *M) error {
	fresh, err := t.Fresh(ctx, m)
	if err != nil {
		return err
	}
	for _, c := range t.Columns {
		reflect.ValueOf(t.Ptr(m, c)).Elem().Set(reflect.ValueOf(t.Ptr(&fresh, c)).Elem())
	}
	t.sync(m)
	return nil
}

// Fresh returns a newly loaded copy of m.
func (t *Table[M]) Fresh(ctx context.Context, m *M) (M, error) {
	return t.Query().WithoutGlobalScopes().Find(ctx, t.key(m))
}

// Replicate returns an unsaved copy of m without its key, timestamps and
// the except columns.
func (t *Table[M]) Replicate(ctx context.Context, m *M, except ...AnyColumn[M]) (M, error) {
	c := *m
	*t.state(&c) = Model{}
	for _, name := range []string{t.PrimaryKey, t.CreatedAt, t.UpdatedAt, t.DeletedAt} {
		if name != "" {
			reflect.ValueOf(t.Ptr(&c, name)).Elem().SetZero()
		}
	}
	for _, col := range except {
		reflect.ValueOf(col.anyPtr(&c)).Elem().SetZero()
	}
	return c, t.fire(ctx, Replicating, &c)
}

// Destroy deletes the models with the given keys, firing events for each.
func (t *Table[M]) Destroy(ctx context.Context, ids ...any) (int, error) {
	models, err := t.Query().FindMany(ctx, ids...)
	if err != nil {
		return 0, err
	}
	for i := range models {
		if err := t.Delete(ctx, &models[i]); err != nil {
			return i, err
		}
	}
	return len(models), nil
}

// ---------------------------------------------------------------------------
// Find-or-create family
// ---------------------------------------------------------------------------

// FirstOrNew finds a model matching attrs, or returns a new unsaved one
// filled with attrs and values.
//
//	Users.Query().FirstOrNew(ctx, orm.Attrs(Users.Email.Set("a@b.c")), Users.Name.Set("A"))
func (q Query[M]) FirstOrNew(ctx context.Context, attrs []Assignment[M], values ...Assignment[M]) (M, error) {
	m, err := q.Where(matching(attrs)...).First(ctx)
	if errors.Is(err, ErrNotFound) {
		var fresh M
		fill(&fresh, attrs, values)
		return fresh, nil
	}
	return m, err
}

// FirstOrCreate finds a model matching attrs or creates it with attrs and values.
func (q Query[M]) FirstOrCreate(ctx context.Context, attrs []Assignment[M], values ...Assignment[M]) (M, error) {
	m, err := q.FirstOrNew(ctx, attrs, values...)
	if err != nil || q.table.state(&m).exists {
		return m, err
	}
	return m, q.Create(ctx, &m)
}

// UpdateOrCreate updates the model matching attrs with values, or creates it.
func (q Query[M]) UpdateOrCreate(ctx context.Context, attrs []Assignment[M], values ...Assignment[M]) (M, error) {
	m, err := q.FirstOrNew(ctx, attrs, values...)
	if err != nil {
		return m, err
	}
	fill(&m, nil, values)
	return m, q.Save(ctx, &m)
}

// CreateOrFirst inserts a model with attrs and values, or — when that
// violates a unique constraint — returns the existing row matching attrs.
// The insert runs in a (nested) transaction so a failure doesn't poison an
// outer Postgres transaction.
func (q Query[M]) CreateOrFirst(ctx context.Context, attrs []Assignment[M], values ...Assignment[M]) (M, error) {
	var m M
	fill(&m, attrs, values)
	err := TransactionOn(ctx, q.connName(), func(ctx context.Context) error { return q.Create(ctx, &m) })
	if IsUniqueViolation(err) {
		return q.Where(matching(attrs)...).First(ctx)
	}
	return m, err
}

// Attrs groups assignments used as match attributes.
func Attrs[M any](as ...Assignment[M]) []Assignment[M] { return as }

func matching[M any](attrs []Assignment[M]) []Cond[M] {
	conds := make([]Cond[M], len(attrs))
	for i, a := range attrs {
		conds[i] = a.match
	}
	return conds
}

func fill[M any](m *M, sets ...[]Assignment[M]) {
	for _, s := range sets {
		for _, a := range s {
			if a.apply != nil {
				a.apply(m)
			}
		}
	}
}

// Fill applies assignments to a model in memory (fill()).
func Fill[M any](m *M, sets ...Assignment[M]) { fill(m, sets) }

// ---------------------------------------------------------------------------
// Mass writes (no model events, like Eloquent's query-level methods)
// ---------------------------------------------------------------------------

// Update updates every matching row, maintaining updated-at.
func (q Query[M]) Update(ctx context.Context, sets ...Assignment[M]) (int64, error) {
	t := q.table
	if t.UpdatedAt != "" && timestamps(ctx) && !slices.ContainsFunc(sets, func(a Assignment[M]) bool { return a.col == t.UpdatedAt }) {
		ts := now()
		sets = append(slices.Clip(sets), Assignment[M]{col: t.UpdatedAt, val: func(b *SQL) { b.Arg(ts) }})
	}
	return q.update(ctx, sets)
}

func (q Query[M]) update(ctx context.Context, sets []Assignment[M]) (int64, error) {
	q = q.prepared()
	res, err := q.exec(ctx, func(b *SQL) {
		b.Write("UPDATE ")
		b.Ident(q.table.Name)
		b.Write(" SET ")
		for i, a := range sets {
			if i > 0 {
				b.Write(", ")
			}
			b.Ident(a.col)
			b.Write(" = ")
			a.val(b)
		}
		q.renderWhere(b)
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Increment adds by to a numeric column on every matching row.
func (q Query[M]) Increment[V Number](ctx context.Context, col Column[M, V], by V, extra ...Assignment[M]) (int64, error) {
	return q.Update(ctx, append([]Assignment[M]{col.SetExpr(Plus(col, by))}, extra...)...)
}

func (q Query[M]) Decrement[V Number](ctx context.Context, col Column[M, V], by V, extra ...Assignment[M]) (int64, error) {
	return q.Update(ctx, append([]Assignment[M]{col.SetExpr(Minus(col, by))}, extra...)...)
}

// Delete deletes every matching row — soft deletes if the table uses them.
func (q Query[M]) Delete(ctx context.Context) (int64, error) {
	if q.table.softDeletes() {
		ts := now()
		return q.Update(ctx, Assignment[M]{col: q.table.DeletedAt, val: func(b *SQL) { b.Arg(ts) }})
	}
	return q.hardDelete(ctx)
}

// ForceDelete permanently deletes every matching row.
func (q Query[M]) ForceDelete(ctx context.Context) (int64, error) { return q.hardDelete(ctx) }

// Restore un-deletes every matching soft deleted row.
func (q Query[M]) Restore(ctx context.Context) (int64, error) {
	return q.WithTrashed().Update(ctx, Assignment[M]{col: q.table.DeletedAt, val: func(b *SQL) { b.Arg(nil) }})
}

func (q Query[M]) hardDelete(ctx context.Context) (int64, error) {
	q = q.prepared()
	res, err := q.exec(ctx, func(b *SQL) {
		b.Write("DELETE FROM ")
		b.Ident(q.table.Name)
		q.renderWhere(b)
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (q Query[M]) renderWhere(b *SQL) {
	if len(q.where) > 0 {
		b.Write(" WHERE ")
		And(q.where...).f(b)
	}
}

// InsertUsing inserts the rows selected by sub (insertUsing).
func (q Query[M]) InsertUsing[O any](ctx context.Context, cols []AnyColumn[M], sub Query[O], selects ...AnyExpr) (int64, error) {
	res, err := q.exec(ctx, func(b *SQL) {
		b.Write("INSERT INTO ")
		b.Ident(q.table.Name)
		b.Write(" (")
		for i, c := range cols {
			if i > 0 {
				b.Write(", ")
			}
			b.Ident(c.Name())
		}
		b.Write(") ")
		sub.renderSelect(b, func(b *SQL) {
			for i, e := range selects {
				if i > 0 {
					b.Write(", ")
				}
				e.build(b)
			}
		})
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Truncate empties the table and resets its auto-increment counter.
func (t *Table[M]) Truncate(ctx context.Context) error {
	q := t.Query()
	c, err := q.conn(ctx)
	if err != nil {
		return err
	}
	for _, stmt := range c.Dialect.Truncate(t.Name) {
		if _, err := q.exec(ctx, func(b *SQL) { b.Write(stmt) }); err != nil &&
			!strings.Contains(err.Error(), "no such table: sqlite_sequence") {
			return err
		}
	}
	return nil
}

// Prune deletes every matching model one by one, firing events — force
// deleting soft deletable models (Prunable). For a mass delete without
// events (MassPrunable) use Delete or ForceDelete.
func (q Query[M]) Prune(ctx context.Context, chunk int) (int, error) {
	t, n := q.table, 0
	for models, err := range q.WithTrashed().chunksByID(ctx, chunk) {
		if err != nil {
			return n, err
		}
		for i := range models {
			del := q.DeleteModel
			if t.softDeletes() {
				del = q.ForceDeleteModel
			}
			if err := del(ctx, &models[i]); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// SaveQuietly, DeleteQuietly, ForceDeleteQuietly and RestoreQuietly skip model events.
func (t *Table[M]) SaveQuietly(ctx context.Context, m *M) error { return t.Save(WithoutEvents(ctx), m) }
func (t *Table[M]) DeleteQuietly(ctx context.Context, m *M) error {
	return t.Delete(WithoutEvents(ctx), m)
}
func (t *Table[M]) ForceDeleteQuietly(ctx context.Context, m *M) error {
	return t.ForceDelete(WithoutEvents(ctx), m)
}
func (t *Table[M]) RestoreQuietly(ctx context.Context, m *M) error {
	return t.Restore(WithoutEvents(ctx), m)
}

type noTimestampsKey struct{}

// WithoutTimestamps returns a ctx in which created/updated timestamps are
// not maintained (Model::withoutTimestamps).
func WithoutTimestamps(ctx context.Context) context.Context {
	return context.WithValue(ctx, noTimestampsKey{}, true)
}

func timestamps(ctx context.Context) bool { return ctx.Value(noTimestampsKey{}) == nil }

// Insert bulk-inserts rows as-is: no events, no timestamps (insert()).
func (q Query[M]) Insert(ctx context.Context, ms ...M) error {
	return q.insertRows(ctx, "", false, nil, nil, ms)
}

// InsertOrIgnore bulk-inserts, skipping rows that violate unique constraints.
func (q Query[M]) InsertOrIgnore(ctx context.Context, ms ...M) error {
	return q.insertRows(ctx, "", true, nil, nil, ms)
}

// Upsert inserts rows or updates the update columns (default: all others)
// when uniqueBy conflicts. Timestamps are maintained.
func (q Query[M]) Upsert(ctx context.Context, ms []M, uniqueBy []AnyColumn[M], update ...AnyColumn[M]) error {
	t := q.table
	ts := now()
	for i := range ms {
		for _, c := range []string{t.CreatedAt, t.UpdatedAt} {
			if c != "" && isZero(t.value(&ms[i], c)) {
				t.setValue(&ms[i], c, ts)
			}
		}
	}
	uniq := names(uniqueBy)
	upd := names(update)
	if len(upd) == 0 {
		for _, c := range t.Columns {
			if !slices.Contains(uniq, c) && c != t.PrimaryKey && c != t.CreatedAt {
				upd = append(upd, c)
			}
		}
	} else if t.UpdatedAt != "" && !slices.Contains(upd, t.UpdatedAt) {
		upd = append(upd, t.UpdatedAt)
	}
	return q.insertRows(ctx, "", false, uniq, upd, ms)
}

func (q Query[M]) insertRows(ctx context.Context, _ string, ignore bool, uniq, upd []string, ms []M) error {
	if len(ms) == 0 {
		return nil
	}
	t := q.table
	cols := slices.Clone(t.Columns)
	if t.KeyType == KeyAutoIncrement && t.PrimaryKey != "" &&
		!slices.ContainsFunc(ms, func(m M) bool { return !isZero(t.key(&m)) }) {
		cols = slices.DeleteFunc(cols, func(c string) bool { return c == t.PrimaryKey })
	}
	ptrs := make([]*M, len(ms))
	for i := range ms {
		ptrs[i] = &ms[i]
	}
	_, err := q.exec(ctx, func(b *SQL) {
		q.renderInsert(b, b.Dialect.InsertVerb(ignore), cols, ptrs)
		if ignore || upd != nil {
			b.Dialect.OnConflict(b, ignore, uniq, upd)
		}
	})
	return err
}

func (q Query[M]) renderInsert(b *SQL, verb string, cols []string, ms []*M) {
	t := q.table
	b.Write(verb, " INTO ")
	b.Ident(t.Name)
	b.Write(" (")
	for i, c := range cols {
		if i > 0 {
			b.Write(", ")
		}
		b.Ident(c)
	}
	b.Write(") VALUES ")
	for r, m := range ms {
		if r > 0 {
			b.Write(", ")
		}
		b.Write("(")
		for i, c := range cols {
			if i > 0 {
				b.Write(", ")
			}
			b.Arg(t.dbValue(m, c))
		}
		b.Write(")")
	}
}

func names[M any](cols []AnyColumn[M]) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name()
	}
	return out
}

func isZero(v any) bool { return v == nil || reflect.ValueOf(v).IsZero() }

// NewULID returns a ULID string (HasUlids): 48-bit millisecond time and
// 80 random bits in Crockford base32.
func NewULID() string {
	const enc = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var u [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		u[i] = byte(ms)
		ms >>= 8
	}
	rand.Read(u[6:])
	hi, lo := binary.BigEndian.Uint64(u[:8]), binary.BigEndian.Uint64(u[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = enc[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// NewUUIDv7 returns a time-ordered UUID string (HasUuids).
func NewUUIDv7() string {
	var u [16]byte
	binary.BigEndian.PutUint64(u[:8], uint64(time.Now().UnixMilli())<<16)
	rand.Read(u[6:])
	u[6] = u[6]&0x0f | 0x70
	u[8] = u[8]&0x3f | 0x80
	h := hex.EncodeToString(u[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
