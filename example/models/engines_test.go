package models_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/stubbedev/eloquent-go/example/models"
	"github.com/stubbedev/eloquent-go/orm"
	"github.com/stubbedev/eloquent-go/schema"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/marcboeker/go-duckdb/v2"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test models: widgets for SQL engines, documents with vectors for stores.
// ---------------------------------------------------------------------------

type Widget struct {
	orm.Model
	ID        int64     `db:"id"`
	Name      string    `db:"name"`
	Kind      string    `db:"kind"`
	Karma     int64     `db:"karma"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

var widgetTable = &orm.Table[Widget]{
	Name:       "widgets",
	PrimaryKey: "id",
	KeyType:    orm.KeyAutoIncrement,
	CreatedAt:  "created_at",
	UpdatedAt:  "updated_at",
	Columns:    []string{"id", "name", "kind", "karma", "created_at", "updated_at"},
	Ptr: func(m *Widget, column string) any {
		switch column {
		case "id":
			return &m.ID
		case "name":
			return &m.Name
		case "kind":
			return &m.Kind
		case "karma":
			return &m.Karma
		case "created_at":
			return &m.CreatedAt
		case "updated_at":
			return &m.UpdatedAt
		}
		return nil
	},
	State: func(m *Widget) *orm.Model { return &m.Model },
}

type WidgetSchema struct {
	*orm.Table[Widget]
	ID        orm.Column[Widget, int64]
	Name      orm.Column[Widget, string]
	Kind      orm.Column[Widget, string]
	Karma     orm.Column[Widget, int64]
	CreatedAt orm.Column[Widget, time.Time]
}

var Widgets = WidgetSchema{
	Table:     widgetTable,
	ID:        orm.NewColumn(widgetTable, "id", func(m *Widget) *int64 { return &m.ID }),
	Name:      orm.NewColumn(widgetTable, "name", func(m *Widget) *string { return &m.Name }),
	Kind:      orm.NewColumn(widgetTable, "kind", func(m *Widget) *string { return &m.Kind }),
	Karma:     orm.NewColumn(widgetTable, "karma", func(m *Widget) *int64 { return &m.Karma }),
	CreatedAt: orm.NewColumn(widgetTable, "created_at", func(m *Widget) *time.Time { return &m.CreatedAt }),
}

type Doc struct {
	orm.Model
	ID    string             `db:"id"`
	Title string             `db:"title"`
	Views int64              `db:"views"`
	Vec   orm.Vector[orm.D3] `db:"vec"`
}

var docTable = &orm.Table[Doc]{
	Name:       "documents",
	PrimaryKey: "id",
	KeyType:    orm.KeyUUID,
	UpdatedAt:  "",
	Columns:    []string{"id", "title", "views", "vec"},
	Ptr: func(m *Doc, column string) any {
		switch column {
		case "id":
			return &m.ID
		case "title":
			return &m.Title
		case "views":
			return &m.Views
		case "vec":
			return &m.Vec
		}
		return nil
	},
	State: func(m *Doc) *orm.Model { return &m.Model },
}

type DocSchema struct {
	*orm.Table[Doc]
	ID    orm.Column[Doc, string]
	Title orm.Column[Doc, string]
	Views orm.Column[Doc, int64]
	Vec   orm.Column[Doc, orm.Vector[orm.D3]]
}

var Docs = DocSchema{
	Table: docTable,
	ID:    orm.NewColumn(docTable, "id", func(m *Doc) *string { return &m.ID }),
	Title: orm.NewColumn(docTable, "title", func(m *Doc) *string { return &m.Title }),
	Views: orm.NewColumn(docTable, "views", func(m *Doc) *int64 { return &m.Views }),
	Vec:   orm.NewColumn(docTable, "vec", func(m *Doc) *orm.Vector[orm.D3] { return &m.Vec }),
}

func createWidgetTable(ctx context.Context, t *testing.T, conn string) {
	t.Helper()
	tCheck(t, schema.On(conn).DropAllTables(ctx))
	tCheck(t, schema.On(conn).Create(ctx, "widgets", func(t *schema.Blueprint) {
		t.ID()
		t.String("name")
		t.String("kind")
		t.BigInteger("karma").Default(0)
		t.Timestamps()
	}))
}

func seedWidgets(t *testing.T) []Widget {
	t.Helper()
	return []Widget{
		{Name: "alpha", Kind: "gear", Karma: 5},
		{Name: "beta", Kind: "spring", Karma: 15},
		{Name: "gamma", Kind: "gear", Karma: 25},
	}
}

func assertWidgets(t *testing.T, ctx context.Context, conn string) {
	t.Helper()
	ws := must(Widgets.Query().On(conn).Where(Widgets.Kind.Eq("gear")).OrderBy(Widgets.Karma.Desc()).Get(ctx))
	eq(t, "where + order", len(ws), 2)
	eq(t, "order desc", ws[0].Name, "gamma")
	n := must(Widgets.Query().On(conn).Count(ctx))
	eq(t, "count", n, int64(3))
	first := must(Widgets.Query().On(conn).Where(Widgets.Name.Like("alp")).First(ctx))
	eq(t, "like", first.Name, "alpha")

	w := first
	w.Karma = 42
	ok(Widgets.Query().On(conn).Save(ctx, &w))
	eq(t, "save reloads", must(Widgets.Query().On(conn).Find(ctx, w.ID)).Karma, int64(42))

	rows := must(orm.From("widgets").On(conn).Where("kind", "=", "gear").OrderBy("name").Get(ctx))
	eq(t, "table query", fmt.Sprint(rows[0]["name"]), "alpha")

	n = must(Widgets.Query().On(conn).Where(Widgets.ID.Eq(w.ID)).Delete(ctx))
	eq(t, "delete", n, int64(1))
	eq(t, "gone", must(Widgets.Query().On(conn).Count(ctx)), int64(2))
}

func TestDuckDB(t *testing.T) {
	current = t
	ctx := context.Background()
	path := t.TempDir() + "/test.duckdb"
	db, err := orm.Open("duck", "duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	createWidgetTable(ctx, t, "duck")
	for i := range seedWidgets(t) {
		var w = seedWidgets(t)[i]
		ok(Widgets.Query().On("duck").Create(ctx, &w))
	}
	assertWidgets(t, ctx, "duck")

	// Flat files: DuckDB reads CSV straight from the builder.
	csv := t.TempDir() + "/flat.csv"
	ok(os.WriteFile(csv, []byte("id,name,kind,karma\n1,alpha,gear,5\n2,beta,spring,15\n3,gamma,gear,25\n"), 0o644))
	rows := must(orm.From(fmt.Sprintf("read_csv('%s')", csv)).On("duck").Where("kind", "=", "gear").Count(ctx))
	eq(t, "csv query", rows, int64(2))

	// Vectors: FLOAT[3] columns and array_distance ordering.
	tCheck(t, schema.On("duck").Create(ctx, "documents", func(t *schema.Blueprint) {
		t.UUID("id")
		t.String("title")
		t.BigInteger("views").Default(0)
		t.Vector("vec", 3)
	}))
	for _, d := range []Doc{
		{Title: "near", Vec: orm.NewVector[orm.D3](1, 0, 0)},
		{Title: "far", Vec: orm.NewVector[orm.D3](10, 10, 10)},
	} {
		ok(Docs.Query().On("duck").Create(ctx, &d))
	}
	found := must(Docs.Query().On("duck").
		OrderBy(Docs.Vec.Nearest(orm.NewVector[orm.D3](1, 0, 0), orm.L2)).
		Limit(1).Get(ctx))
	eq(t, "nearest", found[0].Title, "near")
}

func TestClickHouse(t *testing.T) {
	current = t
	if os.Getenv("ELOQUENT_TEST_CLICKHOUSE") == "" {
		t.Skip("set ELOQUENT_TEST_CLICKHOUSE=clickhouse://default:eloquent@127.0.0.1:19000/eloquent?dial_timeout=5s to run")
	}
	ctx := context.Background()
	db, err := orm.Open("ch", "clickhouse", os.Getenv("ELOQUENT_TEST_CLICKHOUSE"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	createWidgetTable(ctx, t, "ch")
	for i, w := range seedWidgets(t) {
		w.ID = int64(i + 1) // ClickHouse does not generate integer keys
		ok(Widgets.Query().On("ch").Create(ctx, &w))
	}
	ws := must(Widgets.Query().On("ch").Where(Widgets.Kind.Eq("gear")).OrderBy(Widgets.Karma.Desc()).Get(ctx))
	eq(t, "where + order", len(ws), 2)
	eq(t, "order desc", ws[0].Name, "gamma")
	eq(t, "count", must(Widgets.Query().On("ch").Count(ctx)), int64(3))
	n := must(Widgets.Query().On("ch").Where(Widgets.ID.Eq(ws[0].ID)).Update(ctx, Widgets.Karma.Set(99)))
	_ = n // ClickHouse mutations report zero affected rows
	eq(t, "updated", must(Widgets.Query().On("ch").Find(ctx, ws[0].ID)).Karma, int64(99))
	must(Widgets.Query().On("ch").Where(Widgets.ID.Eq(ws[0].ID)).Delete(ctx))
	eq(t, "gone", must(Widgets.Query().On("ch").Count(ctx)), int64(2))
}

func TestMongo(t *testing.T) {
	current = t
	if os.Getenv("ELOQUENT_TEST_MONGO") == "" {
		t.Skip("set ELOQUENT_TEST_MONGO=mongodb://127.0.0.1:17017 to run")
	}
	ctx := context.Background()
	store, err := orm.OpenMongo("mongo", os.Getenv("ELOQUENT_TEST_MONGO"), "eloquent"+strings.ToLower(fmt.Sprint(time.Now().UnixNano()%100000)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(ctx) })

	for _, d := range []Doc{
		{Title: "near", Views: 1, Vec: orm.NewVector[orm.D3](1, 0, 0)},
		{Title: "far", Views: 2, Vec: orm.NewVector[orm.D3](10, 10, 10)},
		{Title: "middle", Views: 3, Vec: orm.NewVector[orm.D3](5, 5, 5)},
	} {
		ok(Docs.Query().On("mongo").Create(ctx, &d))
	}

	eq(t, "count", must(Docs.Query().On("mongo").Count(ctx)), int64(3))
	docs := must(Docs.Query().On("mongo").Where(Docs.Views.Gt(1)).OrderBy(Docs.Views.Asc()).Get(ctx))
	eq(t, "where + order", len(docs), 2)
	eq(t, "order", docs[0].Title, "far")
	eq(t, "vectors hydrate", docs[0].Vec.String(), "[10,10,10]")

	one := must(Docs.Query().On("mongo").Where(Docs.Title.Eq("near")).First(ctx))
	eq(t, "first", one.Views, int64(1))
	eq(t, "exists", must(Docs.Query().On("mongo").Where(Docs.Title.Eq("nope")).DoesntExist(ctx)), true)
	eq(t, "find", must(Docs.Query().On("mongo").Find(ctx, one.ID)).Title, "near")
	eq(t, "in", must(Docs.Query().On("mongo").Where(Docs.Views.In(1, 2)).Count(ctx)), int64(2))
	eq(t, "between", must(Docs.Query().On("mongo").Where(Docs.Views.Between(2, 3)).Count(ctx)), int64(2))
	eq(t, "or", must(Docs.Query().On("mongo").Where(
		orm.Or(Docs.Title.Eq("near"), Docs.Title.Eq("far"))).Count(ctx)), int64(2))
	eq(t, "not", must(Docs.Query().On("mongo").WhereNot(Docs.Title.Eq("near")).Count(ctx)), int64(2))

	one.Views = 10
	ok(Docs.Query().On("mongo").Save(ctx, &one))
	eq(t, "save", must(Docs.Query().On("mongo").Find(ctx, one.ID)).Views, int64(10))
	n := must(Docs.Query().On("mongo").Where(Docs.Views.Eq(3)).Update(ctx, Docs.Title.Set("renamed")))
	eq(t, "update", n, int64(1))
	eq(t, "update applied", must(Docs.Query().On("mongo").Where(Docs.Title.Eq("renamed")).Count(ctx)), int64(1))

	// orm.From works on document stores too.
	rows := must(orm.From("documents").On("mongo").Where("views", ">=", 2).OrderBy("views").Get(ctx))
	eq(t, "table query", len(rows), 3)
	eq(t, "table query values", fmt.Sprint(rows[0]["views"]), "2")
	eq(t, "table count", must(orm.From("documents").On("mongo").Where("title", "=", "renamed").Count(ctx)), int64(1))

	eq(t, "delete", must(Docs.Query().On("mongo").Where(Docs.Title.Eq("renamed")).Delete(ctx)), int64(1))
	eq(t, "deleted", must(Docs.Query().On("mongo").Count(ctx)), int64(2))

	// Joins and raw conditions refuse with a precise error.
	_, err = Users.Query().Join(Posts.Table, Posts.UserID.EqCol(Users.ID)).On("mongo").Get(ctx)
	eq(t, "join refused", orm.IsNotTranslatable(err), true)
	_, err = Docs.Query().On("mongo").Where(orm.Raw[Doc]("views > 1")).Get(ctx)
	eq(t, "raw refused", orm.IsNotTranslatable(err), true)
}

func TestQdrant(t *testing.T) {
	current = t
	if os.Getenv("ELOQUENT_TEST_QDRANT") == "" {
		t.Skip("set ELOQUENT_TEST_QDRANT=http://127.0.0.1:16333 to run")
	}
	ctx := context.Background()
	store, err := orm.OpenQdrant("qdrant", os.Getenv("ELOQUENT_TEST_QDRANT"))
	if err != nil {
		t.Fatal(err)
	}
	ok(store.EnsureCollection(ctx, "documents", 3, orm.Cosine))
	ok(store.DropCollection(ctx, "documents"))
	ok(store.EnsureCollection(ctx, "documents", 3, orm.Cosine))

	for _, d := range []Doc{
		{Title: "near", Views: 1, Vec: orm.NewVector[orm.D3](1, 0, 0)},
		{Title: "far", Views: 2, Vec: orm.NewVector[orm.D3](0, 1, 0)},
		{Title: "middle", Views: 3, Vec: orm.NewVector[orm.D3](0.7, 0.7, 0)},
	} {
		ok(Docs.Query().On("qdrant").Create(ctx, &d))
	}

	eq(t, "count", must(Docs.Query().On("qdrant").Count(ctx)), int64(3))
	eq(t, "filter", must(Docs.Query().On("qdrant").Where(Docs.Views.Gte(2)).Count(ctx)), int64(2))
	eq(t, "in", must(Docs.Query().On("qdrant").Where(Docs.Views.In(1, 3)).Count(ctx)), int64(2))
	eq(t, "range", must(Docs.Query().On("qdrant").Where(Docs.Views.Between(2, 3)).Count(ctx)), int64(2))
	eq(t, "not", must(Docs.Query().On("qdrant").WhereNot(Docs.Title.Eq("near")).Count(ctx)), int64(2))
	eq(t, "or", must(Docs.Query().On("qdrant").Where(
		orm.Or(Docs.Title.Eq("near"), Docs.Title.Eq("far"))).Count(ctx)), int64(2))

	// Nearest-neighbour search: same builder, vector order.
	found := must(Docs.Query().On("qdrant").
		OrderBy(Docs.Vec.Nearest(orm.NewVector[orm.D3](1, 0, 0), orm.Cosine)).
		Limit(2).Get(ctx))
	eq(t, "knn count", len(found), 2)
	eq(t, "nearest first", found[0].Title, "near")

	n := must(Docs.Query().On("qdrant").Where(Docs.Views.Eq(3)).Update(ctx, Docs.Title.Set("renamed")))
	eq(t, "update", n, int64(1))
	eq(t, "updated", must(Docs.Query().On("qdrant").Where(Docs.Title.Eq("renamed")).Count(ctx)), int64(1))

	rows := must(orm.From("documents").On("qdrant").Where("views", "=", 2).Get(ctx))
	eq(t, "table query", len(rows), 1)

	eq(t, "delete", must(Docs.Query().On("qdrant").Where(Docs.Title.Eq("renamed")).Delete(ctx)), int64(1))
	eq(t, "deleted", must(Docs.Query().On("qdrant").Count(ctx)), int64(2))
}

func TestPgVector(t *testing.T) {
	if driver != "pgx" {
		t.Skip("pgvector runs against the postgres container")
	}
	current = t
	ctx := setup(t)
	c, err := orm.Connection(ctx, orm.DefaultConnection)
	tCheck(t, err)
	_, err = c.DB.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	tCheck(t, err)
	tCheck(t, schema.On(orm.DefaultConnection).DropIfExists(ctx, "documents"))
	tCheck(t, schema.On(orm.DefaultConnection).Create(ctx, "documents", func(t *schema.Blueprint) {
		t.UUID("id")
		t.String("title")
		t.BigInteger("views").Default(0)
		t.Vector("vec", 3)
	}))

	for _, d := range []Doc{
		{Title: "near", Vec: orm.NewVector[orm.D3](1, 0, 0)},
		{Title: "far", Vec: orm.NewVector[orm.D3](10, 10, 10)},
	} {
		ok(Docs.Query().Create(ctx, &d))
	}
	found := must(Docs.Query().
		OrderBy(Docs.Vec.Nearest(orm.NewVector[orm.D3](1, 0, 0), orm.L2)).
		Limit(1).Get(ctx))
	eq(t, "knn", found[0].Title, "near")
	eq(t, "vectors round-trip", must(Docs.Query().Where(Docs.Title.Eq("far")).First(ctx)).Vec.String(), "[10,10,10]")
}

func tCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
