package models_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	. "github.com/stubbedev/eloquent-go/example/models"
	"github.com/stubbedev/eloquent-go/orm"
	"github.com/stubbedev/eloquent-go/schema"
)

func TestInsertGetID(t *testing.T) {
	ctx := setup(t)

	id := must(Posts.Query().InsertGetID(ctx, Post{UserID: 2, Title: "raw insert", Published: true}))
	eq(t, "id assigned", id > 0, true)
	eq(t, "row stored", must(Posts.Find(ctx, id)).Title, "raw insert")

	_, err := Users.Query().InsertGetID(ctx, User{ID: 10, Name: "with key"})
	eq(t, "error with key set", err != nil, true)
}

func TestFindOr(t *testing.T) {
	ctx := setup(t)

	eq(t, "findOr hit", must(Users.Query().FindOr(ctx, 1, func() (User, error) {
		return User{Name: "fallback"}, nil
	})).Name, "Alice")
	eq(t, "findOr miss", must(Users.Query().FindOr(ctx, 99, func() (User, error) {
		return User{Name: "fallback"}, nil
	})).Name, "fallback")
}

func TestChunkWhile(t *testing.T) {
	ctx := setup(t)

	var sizes []string
	err := Posts.Query().ChunkWhile(ctx, func(prev, cur Post) bool { return prev.UserID == cur.UserID },
		func(ps []Post) error {
			sizes = append(sizes, fmt.Sprintf("%d:%d", len(ps), ps[0].UserID))
			return nil
		})
	ok(err)
	eq(t, "chunkWhile groups", sizes, []string{"3:1", "1:2", "1:3", "1:5", "1:99"})
}

func TestExplain(t *testing.T) {
	ctx := setup(t)

	rows, err := Users.Where(Users.Karma.Gt(1)).Explain(ctx)
	ok(err)
	eq(t, "explain rows", len(rows) > 0, true)
	eq(t, "explain has columns", len(rows[0]) > 0, true)
}

func TestJSONOverlapConditions(t *testing.T) {
	ctx := setup(t)

	labels := Users.Settings.JSON[[]string]("labels")
	eq(t, "jsonOverlaps", must(Users.Where(labels.JSONOverlaps("admin", "nope")).Count(ctx)), int64(1))
	eq(t, "jsonDoesntContain", must(Users.Where(Users.Settings.JSONDoesntContain("admin")).Count(ctx)), int64(2))
}

func TestQueryLog(t *testing.T) {
	ctx := setup(t)

	orm.EnableQueryLog()
	defer orm.DisableQueryLog()
	must(Users.Query().Count(ctx))
	log := orm.QueryLog()
	eq(t, "query log records", len(log) > 0 && strings.HasPrefix(log[0].SQL, "SELECT"), true)

	orm.FlushQueryLog()
	eq(t, "flush", len(orm.QueryLog()), 0)
}

func TestRetryingTransaction(t *testing.T) {
	ctx := setup(t)

	attempts := 0
	ok(orm.RetryingTransaction(ctx, 3, func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("Error 1213 (40001): Deadlock found when trying to get lock")
		}
		return nil
	}))
	eq(t, "deadlock retried", attempts, 2)

	attempts = 0
	err := orm.RetryingTransaction(ctx, 3, func(ctx context.Context) error {
		attempts++
		return errors.New("boom")
	})
	eq(t, "other errors returned", attempts == 1 && err.Error() == "boom", true)

	eq(t, "IsDeadlock", orm.IsDeadlock(errors.New("SQLSTATE 40P01: deadlock detected")), true)
	eq(t, "not deadlock", orm.IsDeadlock(errors.New("boom")), false)
}

func TestReadWriteSplit(t *testing.T) {
	if driver != "" {
		t.Skip("file-based SQLite only")
	}
	ctx := context.Background()
	dir := t.TempDir()
	readPath, writePath := dir+"/read.db", dir+"/write.db"

	read, err := orm.Open("rwread", "sqlite", "file:"+readPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close() })
	audit, err := orm.Open("audit", "sqlite", "file:"+dir+"/audit.db?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { audit.Close() })
	if _, err := (&schema.Migrator{Connection: "rwread", Migrations: schema.Registered()}).Migrate(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := orm.From("users").On("rwread").Insert(ctx,
		map[string]any{"id": 1, "name": "Alice", "email": "alice@example.com", "active": true, "karma": 42},
	); err != nil {
		t.Fatal(err)
	}

	write, err := orm.OpenReadWrite("rw", "sqlite", "file:"+readPath+"?_time_format=sqlite", "file:"+writePath+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { write.Close() })

	eq(t, "selects read from the read pool", must(Users.Query().On("rw").Count(ctx)), int64(1))
	_, err = Users.Where(Users.ID.Eq(1)).On("rw").Update(ctx, Users.Karma.Set(7))
	eq(t, "writes go to the write pool", err != nil && strings.Contains(err.Error(), "no such table"), true)
}

func TestRowQueryBuilder(t *testing.T) {
	ctx := setup(t)

	rows := must(orm.From("users").
		Where("active", "=", true).
		WhereNull("deleted_at").
		WhereRaw("karma > ?", 10).
		OrderBy("name").
		Get(ctx))
	eq(t, "get", len(rows), 2)
	eq(t, "row values", rows[0]["name"], "Alice")

	eq(t, "count", must(orm.From("users").WhereIn("country_id", 1, 2).Count(ctx)), int64(5))
	eq(t, "exists", must(orm.From("users").Where("email", orm.Eq, "alice@example.com").Exists(ctx)), true)

	// Typed columns work on row queries too: completion lists the model's
	// columns and the compared values are compile-checked.
	typed := must(orm.From("users").
		SelectCols(Users.Name, Users.Karma).
		WhereCond(Users.Active.Eq(true), Users.Karma.Gt(10)).
		WhereCond(Users.DeletedAt.IsNull()).
		OrderByCol(Users.Name).
		Get(ctx))
	eq(t, "typed where", len(typed), 2)
	eq(t, "typed row values", typed[0]["name"], "Alice")
	eq(t, "typed neq", must(orm.From("users").WhereCond(Users.Name.Ne("Alice")).Count(ctx)), int64(4))
	eq(t, "typed order", must(orm.From("users").OrderByColDesc(Users.Karma).Limit(1).Pluck(ctx, "name"))[0], "Carol")
	joined := must(orm.From("users").
		SelectCols(Users.Email).
		JoinTyped(Countries.Table, Users.CountryID, Countries.ID, orm.Eq).
		WhereCond(Countries.Name.Eq("Norway")).
		Count(ctx))
	eq(t, "typed join", joined, int64(2))

	eq(t, "first", must(orm.From("countries").OrderBy("id").First(ctx))["name"], "Denmark")
	eq(t, "pluck", fmt.Sprint(must(orm.From("countries").OrderBy("id").Pluck(ctx, "name"))), "[Denmark Norway]")

	id := must(orm.From("posts").InsertGetID(ctx, map[string]any{"user_id": 2, "title": "row insert", "views": 0}))
	eq(t, "insertGetID", id > 0, true)
	n := must(orm.From("posts").Where("id", "=", id).Update(ctx, map[string]any{"views": 7}))
	eq(t, "update", n, int64(1))
	eq(t, "updated value", must(orm.From("posts").Where("id", "=", id).Pluck(ctx, "views"))[0], any(int64(7)))

	ok(orm.From("users").UpdateOrInsert(ctx,
		map[string]any{"email": "zed@example.com"},
		map[string]any{"name": "Zed", "active": true}))
	z := must(orm.From("users").Where("email", "=", "zed@example.com").First(ctx))
	eq(t, "updateOrInsert inserted", z["name"], "Zed")
	ok(orm.From("users").UpdateOrInsert(ctx,
		map[string]any{"email": "zed@example.com"},
		map[string]any{"name": "Zed Zed"}))
	eq(t, "updateOrInsert updated", must(orm.From("users").Where("email", "=", "zed@example.com").First(ctx))["name"], "Zed Zed")

	eq(t, "delete", must(orm.From("posts").Where("id", "=", id).Delete(ctx)), int64(1))
}

func TestStickyReadsSeeWrites(t *testing.T) {
	if driver != "" {
		t.Skip("file-based SQLite only")
	}
	ctx := context.Background()
	dir := t.TempDir()
	readDSN := "file:" + dir + "/read.db?_time_format=sqlite"
	writeDSN := "file:" + dir + "/write.db?_time_format=sqlite"

	// DDL happens before the split, so both pools get the tables.
	write, err := orm.Open("sticky", "sqlite", writeDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { write.Close() })
	read, err := orm.Open("stickyseed", "sqlite", readDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close() })
	for i, name := range []string{"sticky", "stickyseed"} {
		audit, err := orm.Open("audit", "sqlite", fmt.Sprintf("file:%s/audit%d.db?_time_format=sqlite", dir, i))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { audit.Close() })
		if _, err := (&schema.Migrator{Connection: name, Migrations: schema.Registered()}).Migrate(ctx, false); err != nil {
			t.Fatal(err)
		}
	}

	// The read replica only ever sees the seeded row.
	seedRow := map[string]any{
		"name": "Replica Only", "email": "replica@example.com",
		"active": true, "karma": 1, "created_at": "2026-01-01 10:00:00", "updated_at": "2026-01-01 10:00:00",
	}
	if err := orm.From("users").On("stickyseed").Insert(ctx, seedRow); err != nil {
		t.Fatal(err)
	}

	if _, err := orm.OpenReadWrite("sticky", "sqlite", readDSN, writeDSN); err != nil {
		t.Fatal(err)
	}

	insert := func(ctx context.Context, email string) error {
		return orm.From("users").On("sticky").Insert(ctx, map[string]any{
			"name": "W", "email": email,
			"active": true, "karma": 0, "created_at": "2026-01-02 10:00:00", "updated_at": "2026-01-02 10:00:00",
		})
	}

	// Without Sticky, reads always hit the read pool.
	eq(t, "plain ctx reads the replica", must(Users.Query().On("sticky").Count(ctx)), int64(1))
	ok(insert(ctx, "w1@example.com"))
	eq(t, "plain ctx still reads the replica after a write", must(Users.Query().On("sticky").Count(ctx)), int64(1))

	// With Sticky, reads stay on the replica until the first write,
	// then follow the writes for the rest of the ctx.
	sticky := orm.Sticky(ctx)
	eq(t, "sticky ctx reads the replica before writing", must(Users.Query().On("sticky").Count(sticky)), int64(1))
	ok(insert(sticky, "w2@example.com"))
	eq(t, "sticky ctx reads its own writes", must(Users.Query().On("sticky").Count(sticky)), int64(2))
	eq(t, "table queries are sticky too",
		len(must(orm.From("users").On("sticky").Where("email", "=", "w2@example.com").Get(sticky))), 1)
	eq(t, "other ctxs keep reading the replica", must(Users.Query().On("sticky").Count(ctx)), int64(1))

	// Writes inside a transaction mark the scope too.
	ok(orm.TransactionOn(sticky, "sticky", func(ctx context.Context) error {
		return insert(ctx, "w3@example.com")
	}))
	eq(t, "transaction writes are sticky", must(Users.Query().On("sticky").Count(sticky)), int64(3))
}

func TestEncryptedAndHashedCasts(t *testing.T) {
	ctx := setup(t)

	ok(schema.On(orm.DefaultConnection).Create(ctx, "secrets", func(t *schema.Blueprint) {
		t.String("token")
		t.String("pw")
	}))
	orm.SetEncryptionKey([]byte("test key"))

	tok := orm.Encrypted[map[string]string]{V: map[string]string{"api_key": "s3cret"}}
	var pw orm.Hashed = "hunter2"
	ok(orm.From("secrets").Insert(ctx, map[string]any{"token": tok, "pw": pw}))

	row := must(orm.From("secrets").First(ctx))
	rawTok, rawPw := row["token"].(string), row["pw"].(string)
	eq(t, "encrypted at rest", rawTok != `{"api_key":"s3cret"}`, true)
	eq(t, "hashed at rest", strings.HasPrefix(rawPw, "$2"), true)

	var gotTok orm.Encrypted[map[string]string]
	var gotPw orm.Hashed
	ok(gotTok.Scan(rawTok))
	ok(gotPw.Scan(rawPw))
	eq(t, "decrypts", gotTok.V["api_key"], "s3cret")
	eq(t, "hash verifies", gotPw.Matches("hunter2"), true)
	eq(t, "hash rejects", gotPw.Matches("wrong"), false)
}
