package schema

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/stubbedev/eloquent-go/orm"
)

// Migration is one reversible schema change. Name orders migrations; use the
// Laravel convention 2026_01_31_120000_create_users_table.
type Migration struct {
	Name       string
	Connection string // empty: the migrator's connection
	Up         func(ctx context.Context, s *Builder) error
	Down       func(ctx context.Context, s *Builder) error
	// NoTransaction disables wrapping the migration in a transaction on
	// engines with transactional DDL (SQLite, Postgres).
	NoTransaction bool
}

var (
	regMu    sync.Mutex
	registry []Migration
)

// Register adds migrations to the global registry, typically from init()
// functions in a migrations package (see `make` in Run).
func Register(ms ...Migration) {
	regMu.Lock()
	defer regMu.Unlock()
	registry = append(registry, ms...)
}

// Registered returns the globally registered migrations.
func Registered() []Migration {
	regMu.Lock()
	defer regMu.Unlock()
	return slices.Clone(registry)
}

// Migrator runs migrations and records them in a migrations table.
type Migrator struct {
	Connection string // where the migrations table lives; default connection if empty
	Table      string // default "migrations"
	Migrations []Migration
}

// NewMigrator returns a migrator for ms, or for the registered migrations
// when none are given.
func NewMigrator(ms ...Migration) *Migrator {
	if len(ms) == 0 {
		ms = Registered()
	}
	return &Migrator{Migrations: ms}
}

// Status is one migration's state (migrate:status).
type Status struct {
	Name  string
	Ran   bool
	Batch int
}

func (m *Migrator) conn() string  { return cmp.Or(m.Connection, orm.DefaultConnection) }
func (m *Migrator) table() string { return cmp.Or(m.Table, "migrations") }

func (m *Migrator) sorted() ([]Migration, error) {
	ms := slices.Clone(m.Migrations)
	slices.SortFunc(ms, func(a, b Migration) int { return cmp.Compare(a.Name, b.Name) })
	for i := 1; i < len(ms); i++ {
		if ms[i].Name == ms[i-1].Name {
			return nil, fmt.Errorf("schema: duplicate migration %q", ms[i].Name)
		}
	}
	return ms, nil
}

// Migrate runs pending migrations in a new batch and returns their names.
// With step, each migration gets its own batch (migrate --step).
func (m *Migrator) Migrate(ctx context.Context, step bool) ([]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	ran, err := m.ran(ctx)
	if err != nil {
		return nil, err
	}
	all, err := m.sorted()
	if err != nil {
		return nil, err
	}
	batch := 1
	for _, b := range ran {
		batch = max(batch, b+1)
	}
	var done []string
	for _, mig := range all {
		if _, ok := ran[mig.Name]; ok {
			continue
		}
		if err := m.run(ctx, mig, true, batch); err != nil {
			return done, fmt.Errorf("schema: migrating %s: %w", mig.Name, err)
		}
		done = append(done, mig.Name)
		if step {
			batch++
		}
	}
	return done, nil
}

// Rollback reverts the last batch, or the last steps migrations when steps > 0.
func (m *Migrator) Rollback(ctx context.Context, steps int) ([]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	ran, err := m.ran(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	last := 0
	for name, b := range ran {
		names = append(names, name)
		last = max(last, b)
	}
	// Newest first: by batch, then by name.
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(ran[b], ran[a]), cmp.Compare(b, a))
	})
	var target []string
	for _, n := range names {
		if steps > 0 && len(target) == steps || steps == 0 && ran[n] != last {
			break
		}
		target = append(target, n)
	}
	return m.down(ctx, target)
}

// Reset reverts every migration.
func (m *Migrator) Reset(ctx context.Context) ([]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	all, err := m.sorted()
	if err != nil {
		return nil, err
	}
	ran, err := m.ran(ctx)
	if err != nil {
		return nil, err
	}
	var target []string
	for i := len(all) - 1; i >= 0; i-- {
		if _, ok := ran[all[i].Name]; ok {
			target = append(target, all[i].Name)
		}
	}
	return m.down(ctx, target)
}

// Refresh resets and re-runs every migration.
func (m *Migrator) Refresh(ctx context.Context) ([]string, error) {
	if _, err := m.Reset(ctx); err != nil {
		return nil, err
	}
	return m.Migrate(ctx, false)
}

// Fresh drops every table (without running Down) on every connection the
// migrations use, then migrates from scratch.
func (m *Migrator) Fresh(ctx context.Context) ([]string, error) {
	conns := []string{m.conn()}
	for _, mig := range m.Migrations {
		if c := cmp.Or(mig.Connection, m.conn()); !slices.Contains(conns, c) {
			conns = append(conns, c)
		}
	}
	for _, c := range conns {
		if err := On(c).DropAllTables(ctx); err != nil {
			return nil, err
		}
	}
	return m.Migrate(ctx, false)
}

// Status reports which migrations have run.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	ran, err := m.ran(ctx)
	if err != nil {
		return nil, err
	}
	all, err := m.sorted()
	if err != nil {
		return nil, err
	}
	out := make([]Status, len(all))
	for i, mig := range all {
		b, ok := ran[mig.Name]
		out[i] = Status{Name: mig.Name, Ran: ok, Batch: b}
	}
	return out, nil
}

// Pretend returns the SQL pending migrations would run, without running it.
func (m *Migrator) Pretend(ctx context.Context) (map[string][]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	ran, err := m.ran(ctx)
	if err != nil {
		return nil, err
	}
	all, err := m.sorted()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, mig := range all {
		if _, ok := ran[mig.Name]; ok {
			continue
		}
		b, log := On(cmp.Or(mig.Connection, m.conn())).Pretend()
		if err := mig.Up(ctx, b); err != nil {
			return out, err
		}
		out[mig.Name] = log()
	}
	return out, nil
}

func (m *Migrator) down(ctx context.Context, names []string) ([]string, error) {
	byName := map[string]Migration{}
	for _, mig := range m.Migrations {
		byName[mig.Name] = mig
	}
	var done []string
	for _, n := range names {
		mig, ok := byName[n]
		if !ok {
			return done, fmt.Errorf("schema: migration %q has run but is not registered", n)
		}
		if err := m.run(ctx, mig, false, 0); err != nil {
			return done, fmt.Errorf("schema: rolling back %s: %w", n, err)
		}
		done = append(done, n)
	}
	return done, nil
}

// run applies one migration (up or down) and updates the migrations table,
// inside a transaction when the engine supports transactional DDL and the
// migration and repository share a connection.
func (m *Migrator) run(ctx context.Context, mig Migration, up bool, batch int) error {
	conn := cmp.Or(mig.Connection, m.conn())
	fn := mig.Down
	if up {
		fn = mig.Up
	}
	if fn == nil {
		return errors.New("no Up/Down function")
	}
	apply := func(ctx context.Context) error {
		if err := fn(ctx, On(conn)); err != nil {
			return err
		}
		if up {
			return m.exec(ctx, "INSERT INTO %s (%s, %s) VALUES (%s, %s)", []string{"migration", "batch"}, mig.Name, batch)
		}
		return m.exec(ctx, "DELETE FROM %s WHERE %s = %s", []string{"migration"}, mig.Name)
	}
	c, err := orm.Connection(ctx, conn)
	if err != nil {
		return err
	}
	g, err := grammarFor(c.Dialect.Name())
	if err != nil {
		return err
	}
	if mig.NoTransaction || !g.transactionalDDL() || conn != m.conn() {
		return apply(ctx)
	}
	return orm.TransactionOn(ctx, conn, apply)
}

func (m *Migrator) ensureTable(ctx context.Context) error {
	return On(m.conn()).CreateIfNotExists(ctx, m.table(), func(t *Blueprint) {
		t.Increments("id")
		t.String("migration")
		t.Integer("batch")
	})
}

func (m *Migrator) ran(ctx context.Context) (map[string]int, error) {
	c, err := orm.Connection(ctx, m.conn())
	if err != nil {
		return nil, err
	}
	g, _ := grammarFor(c.Dialect.Name())
	rows, err := c.DB.QueryContext(ctx, fmt.Sprintf("SELECT %s, %s FROM %s",
		g.quote("migration"), g.quote("batch"), g.quote(m.table())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var batch int
		if err := rows.Scan(&name, &batch); err != nil {
			return nil, err
		}
		out[name] = batch
	}
	return out, rows.Err()
}

// exec formats a statement against the migrations table: the first %s is
// the table, then the quoted columns, then one placeholder per arg.
func (m *Migrator) exec(ctx context.Context, format string, cols []string, args ...any) error {
	c, err := orm.Connection(ctx, m.conn())
	if err != nil {
		return err
	}
	g, _ := grammarFor(c.Dialect.Name())
	parts := []any{g.quote(m.table())}
	for _, col := range cols {
		parts = append(parts, g.quote(col))
	}
	for i := range args {
		parts = append(parts, c.Dialect.Placeholder(i+1))
	}
	_, err = c.DB.ExecContext(ctx, fmt.Sprintf(format, parts...), args...)
	return err
}
