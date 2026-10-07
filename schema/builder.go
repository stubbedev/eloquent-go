package schema

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/stubbedev/eloquent-go/orm"
)

// Builder runs schema operations on one connection (the Schema facade).
type Builder struct {
	conn    string
	pretend *[]string // when set, statements are collected instead of run
}

// On returns a builder for a named connection (Schema::connection()).
func On(conn string) *Builder { return &Builder{conn: conn} }

// Default returns a builder for the default connection.
func Default() *Builder { return On(orm.DefaultConnection) }

// Package-level shortcuts on the default connection.

func Create(ctx context.Context, table string, fn func(*Blueprint)) error {
	return Default().Create(ctx, table, fn)
}

func Table(ctx context.Context, table string, fn func(*Blueprint)) error {
	return Default().Table(ctx, table, fn)
}

func Drop(ctx context.Context, table string) error { return Default().Drop(ctx, table) }

func DropIfExists(ctx context.Context, table string) error {
	return Default().DropIfExists(ctx, table)
}

func Rename(ctx context.Context, from, to string) error { return Default().Rename(ctx, from, to) }

func HasTable(ctx context.Context, table string) (bool, error) {
	return Default().HasTable(ctx, table)
}

func HasColumn(ctx context.Context, table, column string) (bool, error) {
	return Default().HasColumn(ctx, table, column)
}

// Create creates table from the blueprint built by fn.
func (s *Builder) Create(ctx context.Context, table string, fn func(*Blueprint)) error {
	bp := &Blueprint{table: table, create: true}
	fn(bp)
	return s.run(ctx, func(g grammar) ([]string, error) { return g.compileCreate(bp) })
}

// CreateIfNotExists is Create with IF NOT EXISTS.
func (s *Builder) CreateIfNotExists(ctx context.Context, table string, fn func(*Blueprint)) error {
	bp := &Blueprint{table: table, create: true, ifNot: true}
	fn(bp)
	return s.run(ctx, func(g grammar) ([]string, error) { return g.compileCreate(bp) })
}

// Table alters an existing table: add, change, rename or drop columns,
// indexes and keys. On SQLite, alterations ALTER TABLE cannot express
// rebuild the table.
func (s *Builder) Table(ctx context.Context, table string, fn func(*Blueprint)) error {
	bp := &Blueprint{table: table}
	fn(bp)
	_, g, err := s.grammar(ctx)
	if err != nil {
		return err
	}
	stmts, err := g.compileAlter(bp)
	if errors.Is(err, errRebuild) {
		return s.rebuildSQLite(ctx, g, bp)
	}
	if err != nil {
		return err
	}
	return s.exec(ctx, stmts)
}

func (s *Builder) Drop(ctx context.Context, table string) error {
	return s.run(ctx, func(g grammar) ([]string, error) { return []string{g.drop(table, false)}, nil })
}

func (s *Builder) DropIfExists(ctx context.Context, table string) error {
	return s.run(ctx, func(g grammar) ([]string, error) { return []string{g.drop(table, true)}, nil })
}

// DropColumns drops columns from table.
func (s *Builder) DropColumns(ctx context.Context, table string, columns ...string) error {
	return s.Table(ctx, table, func(t *Blueprint) { t.DropColumn(columns...) })
}

func (s *Builder) Rename(ctx context.Context, from, to string) error {
	return s.run(ctx, func(g grammar) ([]string, error) { return []string{g.rename(from, to)}, nil })
}

func (s *Builder) HasTable(ctx context.Context, table string) (bool, error) {
	tables, err := s.Tables(ctx)
	return slices.Contains(tables, table), err
}

func (s *Builder) HasColumn(ctx context.Context, table, column string) (bool, error) {
	return s.HasColumns(ctx, table, column)
}

// HasColumns reports whether table has all columns.
func (s *Builder) HasColumns(ctx context.Context, table string, columns ...string) (bool, error) {
	cols, err := s.Columns(ctx, table)
	if err != nil {
		return false, err
	}
	for _, c := range columns {
		if !slices.Contains(cols, c) {
			return false, nil
		}
	}
	return true, nil
}

// WhenTableHasColumn alters table only if it has column.
func (s *Builder) WhenTableHasColumn(ctx context.Context, table, column string, fn func(*Blueprint)) error {
	ok, err := s.HasColumn(ctx, table, column)
	if err != nil || !ok {
		return err
	}
	return s.Table(ctx, table, fn)
}

// WhenTableDoesntHaveColumn alters table only if it lacks column.
func (s *Builder) WhenTableDoesntHaveColumn(ctx context.Context, table, column string, fn func(*Blueprint)) error {
	ok, err := s.HasColumn(ctx, table, column)
	if err != nil || ok {
		return err
	}
	return s.Table(ctx, table, fn)
}

// DropAllTables drops every table on the connection (used by migrate:fresh).
func (s *Builder) DropAllTables(ctx context.Context) error {
	tables, err := s.Tables(ctx)
	if err != nil || len(tables) == 0 {
		return err
	}
	return s.WithoutForeignKeyConstraints(ctx, func(ctx context.Context) error {
		return s.run(ctx, func(g grammar) ([]string, error) {
			var stmts []string
			for _, t := range tables {
				stmts = append(stmts, g.drop(t, true))
			}
			return stmts, nil
		})
	})
}

func (s *Builder) EnableForeignKeyConstraints(ctx context.Context) error {
	return s.run(ctx, func(g grammar) ([]string, error) { return []string{g.foreignKeyChecks(true)}, nil })
}

func (s *Builder) DisableForeignKeyConstraints(ctx context.Context) error {
	return s.run(ctx, func(g grammar) ([]string, error) { return []string{g.foreignKeyChecks(false)}, nil })
}

// WithoutForeignKeyConstraints runs fn with constraint checks disabled.
func (s *Builder) WithoutForeignKeyConstraints(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := s.DisableForeignKeyConstraints(ctx); err != nil {
		return err
	}
	err := fn(ctx)
	return errors.Join(err, s.EnableForeignKeyConstraints(ctx))
}

// DriverName returns the connection's dialect name: "sqlite", "postgres"
// or "mysql" (getDriverName()), for migrations that differ per engine.
func (s *Builder) DriverName(ctx context.Context) (string, error) { return s.kind(ctx) }

// Pretend returns a builder that records SQL instead of executing it, and
// a function returning what was recorded.
func (s *Builder) Pretend() (*Builder, func() []string) {
	var log []string
	return &Builder{conn: s.conn, pretend: &log}, func() []string { return log }
}

func (s *Builder) grammar(ctx context.Context) (orm.Conn, grammar, error) {
	c, err := orm.Connection(ctx, s.conn)
	if err != nil {
		return c, grammar{}, err
	}
	g, err := grammarFor(c.Dialect.Name())
	return c, g, err
}

func (s *Builder) run(ctx context.Context, compile func(grammar) ([]string, error)) error {
	_, g, err := s.grammar(ctx)
	if err != nil {
		return err
	}
	stmts, err := compile(g)
	if err != nil {
		return err
	}
	return s.exec(ctx, stmts)
}

func (s *Builder) exec(ctx context.Context, stmts []string) error {
	c, err := orm.Connection(ctx, s.conn)
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if stmt == "" {
			continue
		}
		if s.pretend != nil {
			*s.pretend = append(*s.pretend, stmt)
			continue
		}
		start := time.Now()
		_, err := c.DB.ExecContext(ctx, stmt)
		orm.Emit(orm.QueryEvent{Connection: s.conn, SQL: stmt, Duration: time.Since(start), Err: err})
		if err != nil {
			return &orm.QueryError{Err: err, SQL: stmt}
		}
	}
	return nil
}
