package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/eloquent-go/orm"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Like the model tests, these run on SQLite unless ELOQUENT_TEST_DRIVER and
// ELOQUENT_TEST_DSN point at another engine (see compose.yaml and Makefile).
var (
	driver   = os.Getenv("ELOQUENT_TEST_DRIVER")
	openOnce sync.Once
)

func setup(t *testing.T) (context.Context, *Builder) {
	t.Helper()
	ctx := context.Background()
	if driver == "" || driver == "sqlite" {
		db, err := sql.Open("sqlite", "file::memory:?_time_format=sqlite")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		orm.SetDefault(db, orm.SQLite)
		return ctx, Default()
	}
	openOnce.Do(func() {
		if _, err := orm.Open(orm.DefaultConnection, driver, os.Getenv("ELOQUENT_TEST_DSN")); err != nil {
			t.Fatal(err)
		}
	})
	if err := Default().DropAllTables(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, Default()
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func want[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if g, w := fmt.Sprint(got), fmt.Sprint(want); g != w {
		t.Errorf("%s:\n got  %s\n want %s", what, g, w)
	}
}

// TestGrammarGolden pins the DDL each grammar produces for one blueprint
// exercising most column types, modifiers, indexes and alterations.
func TestGrammarGolden(t *testing.T) {
	ctx := context.Background()
	for _, d := range []orm.Dialect{orm.Postgres, orm.MySQL} {
		name := "golden-" + d.Name()
		orm.AddConnection(name, nil, d) // pretend mode never touches the DB
		b, log := On(name).Pretend()
		check(t, b.Create(ctx, "flights", flightsBlueprint))
		check(t, b.Table(ctx, "flights", alterBlueprint))
		golden, err := os.ReadFile(filepath.Join("testdata", d.Name()+".sql"))
		check(t, err)
		wantLines := strings.Split(strings.TrimSpace(string(golden)), "\n")
		if got := log(); !slices.Equal(got, wantLines) {
			t.Errorf("%s DDL differs from testdata/%s.sql:\n%s", d.Name(), d.Name(), strings.Join(got, "\n"))
		}
	}

	orm.AddConnection("golden-sqlite", nil, orm.SQLite)
	b, log := On("golden-sqlite").Pretend()
	check(t, b.Create(ctx, "t", func(t *Blueprint) {
		t.ID()
		t.Enum("status", "a", "b")
		t.String("email").Unique()
		t.Morphs("owner")
	}))
	_ = log
	err := b.Create(ctx, "t", func(t *Blueprint) { t.Text("body").FullText() })
	want(t, "sqlite full text unsupported", err != nil && strings.Contains(err.Error(), "full text"), true)
}

func TestLiveSchema(t *testing.T) {
	ctx, s := setup(t)
	check(t, s.Create(ctx, "airlines", func(t *Blueprint) { t.ID(); t.String("name").Nullable() }))
	check(t, s.Create(ctx, "carriers", func(t *Blueprint) { t.ID() }))
	check(t, s.Create(ctx, "flights", flightsBlueprint))

	c, err := orm.Connection(ctx, orm.DefaultConnection)
	check(t, err)
	exec := func(q string, args ...any) {
		t.Helper()
		stmt, _ := render(c.Dialect, q)
		_, err := c.DB.ExecContext(ctx, stmt, args...)
		check(t, err)
	}
	exec("INSERT INTO airlines (id) VALUES (1)")
	exec("INSERT INTO carriers (id) VALUES (1)")
	exec("INSERT INTO flights (airline_id, name, code, status, ref, seats, notes) VALUES (1, ?, ?, ?, ?, 3, ?)",
		"KL1234", "KLM", "scheduled", "0190d0f5-0000-7000-8000-000000000000", "window seats")

	ok, err := s.HasTable(ctx, "flights")
	check(t, err)
	want(t, "hasTable", ok, true)
	ok, err = s.HasColumns(ctx, "flights", "code", "price_cents", "deleted_at")
	check(t, err)
	want(t, "hasColumns", ok, true)

	cols, err := s.GetColumns(ctx, "flights")
	check(t, err)
	idCol := cols[slices.IndexFunc(cols, func(c ColumnInfo) bool { return c.Name == "id" })]
	want(t, "id auto increment", idCol.AutoIncrement, true)
	meta := cols[slices.IndexFunc(cols, func(c ColumnInfo) bool { return c.Name == "meta" })]
	want(t, "nullable", meta.Nullable, true)

	ok, err = s.HasIndex(ctx, "flights", []string{"code"}, "unique")
	check(t, err)
	want(t, "unique index", ok, true)
	ok, err = s.HasIndex(ctx, "flights", []string{"flights_airline_departs"})
	check(t, err)
	want(t, "named index", ok, true)
	fks, err := s.GetForeignKeys(ctx, "flights")
	check(t, err)
	want(t, "foreign key", len(fks) == 1 && fks[0].ForeignTable == "airlines" && fks[0].OnDelete == "cascade", true)

	// Alter: add, change, rename, drop columns; rename index; swap the
	// foreign key. SQLite does this by rebuilding the table.
	check(t, s.Table(ctx, "flights", alterBlueprint))

	names, err := s.Columns(ctx, "flights")
	check(t, err)
	want(t, "added gate", slices.Contains(names, "gate"), true)
	want(t, "renamed notes", slices.Contains(names, "remarks") && !slices.Contains(names, "notes"), true)
	want(t, "dropped ref", slices.Contains(names, "ref"), false)
	want(t, "kept generated column", slices.Contains(names, "price_cents"), true)
	ok, err = s.HasIndex(ctx, "flights", []string{"flights_code_uq"}, "unique")
	check(t, err)
	want(t, "renamed index", ok, true)
	fks, err = s.GetForeignKeys(ctx, "flights")
	check(t, err)
	want(t, "swapped foreign key", len(fks) == 1 && fks[0].ForeignTable == "carriers" && fks[0].OnDelete == "set null", true)

	var name, remarks string
	stmt, _ := render(c.Dialect, "SELECT name, remarks FROM flights")
	rows, err := c.DB.QueryContext(ctx, stmt)
	check(t, err)
	rows.Next()
	check(t, rows.Scan(&name, &remarks))
	rows.Close()
	want(t, "data preserved", name+"/"+remarks, "KL1234/window seats")

	// Primary keys on a pivot-style table.
	check(t, s.Create(ctx, "pairs", func(t *Blueprint) { t.Integer("a"); t.Integer("b") }))
	check(t, s.Table(ctx, "pairs", func(t *Blueprint) { t.Primary("a", "b") }))
	ok, err = s.HasIndex(ctx, "pairs", []string{"a", "b"}, "primary")
	check(t, err)
	want(t, "add primary", ok, true)

	check(t, s.WhenTableDoesntHaveColumn(ctx, "pairs", "c", func(t *Blueprint) { t.Integer("c").Nullable() }))
	check(t, s.WhenTableHasColumn(ctx, "pairs", "c", func(t *Blueprint) { t.Index("c") }))
	ok, err = s.HasIndex(ctx, "pairs", []string{"c"})
	check(t, err)
	want(t, "conditional alter", ok, true)
	check(t, s.Table(ctx, "pairs", func(t *Blueprint) { t.DropIndex("c") }))
	check(t, s.DropColumns(ctx, "pairs", "c"))
	ok, err = s.HasColumn(ctx, "pairs", "c")
	check(t, err)
	want(t, "dropColumns", ok, false)

	if driver != "" && driver != "sqlite" {
		check(t, s.Table(ctx, "flights", func(t *Blueprint) { t.FullText("remarks") }))
		ok, err = s.HasIndex(ctx, "flights", []string{"flights_remarks_fulltext"})
		check(t, err)
		want(t, "full text index", ok, true)
	}

	check(t, s.Rename(ctx, "pairs", "couples"))
	ok, err = s.HasTable(ctx, "couples")
	check(t, err)
	want(t, "rename table", ok, true)
	check(t, s.Drop(ctx, "couples"))
	check(t, s.DropIfExists(ctx, "couples"))
	check(t, s.WithoutForeignKeyConstraints(ctx, func(ctx context.Context) error { return s.Drop(ctx, "airlines") }))
}

func migration(name, table string) Migration {
	return Migration{
		Name: name,
		Up: func(ctx context.Context, s *Builder) error {
			return s.Create(ctx, table, func(t *Blueprint) { t.ID(); t.Timestamps() })
		},
		Down: func(ctx context.Context, s *Builder) error { return s.DropIfExists(ctx, table) },
	}
}

func TestMigrator(t *testing.T) {
	ctx, s := setup(t)
	m := &Migrator{Migrations: []Migration{
		migration("2026_01_01_000001_create_a_table", "a"),
		migration("2026_01_01_000002_create_b_table", "b"),
		migration("2026_01_01_000003_create_c_table", "c"),
	}}
	tables := func() string {
		ts, err := s.Tables(ctx)
		check(t, err)
		return strings.Join(slices.DeleteFunc(ts, func(t string) bool { return t == "migrations" }), ",")
	}

	pending, err := m.Pretend(ctx)
	check(t, err)
	want(t, "pretend", len(pending) == 3 && strings.Contains(pending["2026_01_01_000001_create_a_table"][0], "CREATE TABLE"), true)
	want(t, "pretend ran nothing", tables(), "")

	ran, err := m.Migrate(ctx, false)
	check(t, err)
	want(t, "migrate", len(ran), 3)
	want(t, "tables", tables(), "a,b,c")

	m.Migrations = append(m.Migrations, migration("2026_01_01_000004_create_d_table", "d"))
	_, err = m.Migrate(ctx, false)
	check(t, err)
	st, err := m.Status(ctx)
	check(t, err)
	want(t, "batches", st[0].Batch == 1 && st[3].Batch == 2, true)

	back, err := m.Rollback(ctx, 0)
	check(t, err)
	want(t, "rollback last batch", back, []string{"2026_01_01_000004_create_d_table"})
	back, err = m.Rollback(ctx, 2)
	check(t, err)
	want(t, "rollback steps", back, []string{"2026_01_01_000003_create_c_table", "2026_01_01_000002_create_b_table"})
	want(t, "after rollback", tables(), "a")

	_, err = m.Migrate(ctx, true)
	check(t, err)
	st, err = m.Status(ctx)
	check(t, err)
	want(t, "step batches", st[1].Batch != st[2].Batch, true)

	_, err = m.Refresh(ctx)
	check(t, err)
	want(t, "refresh", tables(), "a,b,c,d")
	_, err = m.Fresh(ctx)
	check(t, err)
	want(t, "fresh", tables(), "a,b,c,d")
	back, err = m.Reset(ctx)
	check(t, err)
	want(t, "reset", len(back), 4)
	want(t, "after reset", tables(), "")

	// A failing migration is rolled back where DDL is transactional, and is
	// never recorded as run.
	failing := Migration{
		Name: "2026_01_01_000005_broken",
		Up: func(ctx context.Context, s *Builder) error {
			if err := s.Create(ctx, "half", func(t *Blueprint) { t.ID() }); err != nil {
				return err
			}
			return errors.New("boom")
		},
		Down: func(context.Context, *Builder) error { return nil },
	}
	m.Migrations = []Migration{failing}
	_, err = m.Migrate(ctx, false)
	want(t, "failing migration", err != nil && strings.Contains(err.Error(), "boom"), true)
	st, err = m.Status(ctx)
	check(t, err)
	want(t, "not recorded", st[0].Ran, false)
	if driver != "mysql" {
		want(t, "ddl rolled back", tables(), "")
	}
}

func TestRunCLI(t *testing.T) {
	ctx, _ := setup(t)
	m := &Migrator{Migrations: []Migration{migration("2026_01_01_000001_create_a_table", "a")}}
	var out bytes.Buffer
	check(t, Run(ctx, m, []string{"status"}, &out))
	want(t, "status pending", strings.Contains(out.String(), "Pending"), true)
	out.Reset()
	check(t, Run(ctx, m, []string{"migrate"}, &out))
	want(t, "migrate output", strings.Contains(out.String(), "Migrated"), true)
	out.Reset()
	check(t, Run(ctx, m, []string{"migrate:rollback"}, &out))
	want(t, "rollback output", strings.Contains(out.String(), "Rolled back"), true)

	dir := t.TempDir()
	out.Reset()
	check(t, Run(ctx, m, []string{"make", "create_flights_table", "-dir", dir}, &out))
	files, _ := filepath.Glob(filepath.Join(dir, "*_create_flights_table.go"))
	want(t, "make created file", len(files), 1)
	src, _ := os.ReadFile(files[0])
	want(t, "make scaffolds create", strings.Contains(string(src), `s.Create(ctx, "flights"`), true)

	path, err := MakeMigration(dir, "migrations", "add_votes_to_users_table", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	check(t, err)
	src, _ = os.ReadFile(path)
	want(t, "make scaffolds alter", strings.Contains(string(src), `s.Table(ctx, "users"`) && strings.HasSuffix(path, "2026_01_02_030405_add_votes_to_users_table.go"), true)
}

// render rewrites ? placeholders for the dialect, for the raw SQL in tests.
func render(d orm.Dialect, q string) (string, struct{}) {
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString(d.Placeholder(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), struct{}{}
}
