# eloquent-go

An Eloquent-style ORM, schema builder and migrator for Go, built on Go 1.27
generic methods and fully type-checked.

```go
users, err := Users.Query().
	Scope(Active).
	Where(Users.Karma.Gt(10)).
	WhereHas(UserPosts, Published).
	With(UserPosts, func(q orm.Query[Post]) orm.Query[Post] {
		return q.OrderBy(Posts.Views.Desc()).With(PostComments)
	}).
	WithCount(UserPosts, Users.PostsCount).
	Latest().
	Paginate(ctx, 20, page)

emails, err := Users.Query().Pluck(ctx, Users.Email) // []string, inferred from the column
total, err := Posts.Query().Sum(ctx, Posts.Views)     // int64; Sum(ctx, Users.Email) does not compile
```

Every column is typed (`orm.Column[User, string]`), so these mistakes are
compile errors rather than runtime SQL errors:

```go
Users.Query().Where(Posts.Title.Eq("x"))       // a Post condition in a User query
Users.Query().Where(Users.Karma.Eq("lots"))    // string for an int column
Users.Query().Sum(ctx, Users.Email)            // string does not satisfy orm.Number
Users.Query().With(PostComments)               // a Post relation on a User query
```

Supported databases are **SQLite**, **PostgreSQL** and **MySQL / MariaDB**.
The dialect follows the connection, and the same query renders correctly for
each. The full test suite runs against all four engines.

## Contents

- [Requirements and install](#requirements-and-install)
- [Quick start](#quick-start)
- [Packages](#packages)
- [Eloquent feature map](#eloquent-feature-map)
- [What Go doesn't allow](#what-go-doesnt-allow)
- [Testing](#testing)
- [Cookbook](docs/cookbook.md): recipes for every feature

## Requirements and install

Go 1.27 or newer is required, because the API relies on generic methods.

```sh
go get github.com/stubbedev/eloquent-go
```

Bring your own `database/sql` driver: `modernc.org/sqlite` (or any SQLite
driver), `github.com/jackc/pgx/v5/stdlib`, or `github.com/go-sql-driver/mysql`.

## Quick start

**1. Define models.** A model is a plain struct that embeds `orm.Model`,
plus a directive. The directive options mirror Laravel 13's `#[Table]` /
`#[Connection]` attributes and the `SoftDeletes` / `HasUuids` traits.

```go
package models

//go:generate go run github.com/stubbedev/eloquent-go/cmd/ormgen

//orm:table users soft_deletes
type User struct {
	orm.Model
	ID         int64      `db:"id"`
	CountryID  int64      `db:"country_id,nullzero"` // 0 is written as NULL
	Name       string     `db:"name"`
	Email      string     `db:"email"`
	Active     bool       `db:"active"`
	CreatedAt  time.Time  `db:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at"`
	DeletedAt  *time.Time `db:"deleted_at"`
	PostsCount int64      `db:"posts_count,virtual"` // filled by WithCount

	Posts []Post // relations have no db tag
}
```

`go generate` writes `orm_gen.go`, which defines a `Users` schema value with
typed columns (`Users.Email`, `Users.CreatedAt`, ...) and table metadata.

**2. Declare relations and scopes.**

```go
var UserPosts = orm.HasMany(Posts.Table, Users.ID, Posts.UserID,
	func(u *User, ps []Post) { u.Posts = ps })

func Active(q orm.Query[User]) orm.Query[User] { return q.Where(Users.Active.Eq(true)) }
```

**3. Connect once.** The driver picks the dialect.

```go
orm.Open(orm.DefaultConnection, "pgx", os.Getenv("DATABASE_URL"))
orm.Open("analytics", "mysql", os.Getenv("ANALYTICS_DSN")) // used by //orm:table x connection=analytics
```

**4. Migrate.**

```go
schema.Register(schema.Migration{
	Name: "2026_01_01_000000_create_users_table",
	Up: func(ctx context.Context, s *schema.Builder) error {
		return s.Create(ctx, "users", func(t *schema.Blueprint) {
			t.ID()
			t.ForeignID("country_id").Nullable().Constrained().NullOnDelete()
			t.String("name")
			t.String("email").Unique()
			t.Timestamps()
			t.SoftDeletes()
		})
	},
	Down: func(ctx context.Context, s *schema.Builder) error { return s.DropIfExists(ctx, "users") },
})

schema.Run(ctx, schema.NewMigrator(), os.Args[1:], os.Stdout) // migrate | rollback | fresh | status | make ...
```

**5. Query.** No database handle is passed around; connections resolve by
name, and transactions travel in `ctx`.

```go
u := User{Name: "Ada", Email: "ada@example.com"}
err := Users.Create(ctx, &u)               // u.ID, timestamps filled in

err = orm.Transaction(ctx, func(ctx context.Context) error {
	u.Name = "Ada Lovelace"
	return Users.Save(ctx, &u)              // updates only dirty columns
})
```

The `example/` directory contains a complete schema covering every relation
type, its migrations, a migration CLI (`go run ./example/migrate`), and the
test suite that exercises all of it.

## Packages

| Package | Purpose |
|---|---|
| `orm` | Query builder, models, relations, events, scopes, pagination, transactions, factories |
| `schema` | Blueprint DDL, introspection, migrations, artisan-style CLI (`schema.Run`) |
| `cmd/ormgen` | Generates typed columns and table metadata from model structs |

## Eloquent feature map

| Eloquent | eloquent-go |
|---|---|
| `User::query()`, `User::where(...)` | `Users.Query()`, `Users.Where(...)` |
| `where`, `orWhere`, `whereNot`, nested closures | `Where`, `OrWhere`, `WhereNot`, `orm.And` / `orm.Or` |
| `whereIn`, `whereBetween`, `whereNull`, `whereLike` | `Col.In`, `Col.Between`, `Col.IsNull`, `Col.Like` |
| `whereColumn` | `Col.EqCol(other)`, `Col.Cmp(op, other)` |
| `whereDate/Year/Month/Day/Time` | `orm.Date(col).Eq(...)`, `orm.Year(col)`, ... |
| `whereJsonContains/Length/ContainsKey`, `->` paths | `Col.JSON[T]("a","b")`, `.JSONContains`, `.JSONLength`, `.JSONHasKey` |
| `whereFullText` | `WhereFullText(term, cols...)` |
| `whereAny`, `whereAll` | `orm.AnyOf`, `orm.AllOf` |
| `whereExists`, `whereIn(subquery)` | `orm.Exists`, `Col.InSub(q.Subquery(col))` |
| `select`, `addSelect`, `selectRaw`, `distinct` | `Select`, `AddSelect`, `AddSelectAs(col, expr)`, `Distinct` |
| `join`, `leftJoin`, `rightJoin`, `crossJoin`, `joinSub` | `Join`, `LeftJoin`, `RightJoin`, `CrossJoin`, `JoinSub` |
| `groupBy`, `having`, `orderBy`, `latest`, `inRandomOrder`, `reorder` | same names |
| `union`, `unionAll`, `when`, `unless`, `lockForUpdate`, `sharedLock` | same names |
| `get`, `first`, `firstOrFail`, `find`, `findMany`, `sole`, `value`, `pluck` | `Get`, `First` (`ErrNotFound`), `Find`, `FindMany`, `Sole`, `Value`, `Pluck`, `PluckMap` |
| `count`, `sum`, `avg`, `min`, `max`, `exists` | same names; `Sum` and `Avg` only accept numeric columns |
| `chunk`, `chunkById`, `lazy`, `cursor` | `Chunk`, `ChunkByID`, `Lazy` and `Cursor` (Go iterators) |
| `paginate`, `simplePaginate`, `cursorPaginate` | same names |
| `create`, `save`, `update`, `delete`, `destroy` | `Create`, `Save`, `Update`, `Delete`, `Destroy` |
| `insert`, `insertOrIgnore`, `insertUsing`, `upsert` | same names |
| `increment`, `decrement`, `truncate` | `Increment`, `Decrement`, `Truncate` |
| `firstOrNew/Create`, `updateOrCreate`, `createOrFirst` | same names with typed `Attrs(...)` |
| `isDirty`, `isClean`, `wasChanged`, `getOriginal` | `Users.IsDirty(&u)`, `Users.Email.IsDirty(&u)`, `.Original(&u)` |
| `replicate`, `refresh`, `fresh`, `touch`, `is`, `isNot` | same names |
| Soft deletes, `withTrashed`, `onlyTrashed`, `restore`, `forceDelete` | `soft_deletes` directive and the same methods |
| Timestamps, `withoutTimestamps` | automatic; `orm.WithoutTimestamps(ctx)` |
| UUID / ULID keys | `key_type=uuid` / `key_type=ulid` |
| Local / dynamic / global scopes, `withoutGlobalScope(s)` | `Scope(fn)`, `AddGlobalScope`, `WithoutGlobalScope(s)` |
| Events, observers, `withoutEvents`, `saveQuietly` | `Users.On(orm.Creating, fn)`, `Users.Observe(obs)`, `orm.WithoutEvents(ctx)`, `SaveQuietly` |
| `hasOne`, `hasMany`, `belongsTo`, `belongsToMany` (+ pivot) | `orm.HasOne`, `HasMany`, `BelongsTo`, `BelongsToMany` (typed pivot model) |
| `hasOneThrough`, `hasManyThrough` | `orm.HasOneThrough`, `HasManyThrough` |
| `morphOne`, `morphMany`, `morphTo`, `morphToMany`, `morphedByMany` | same names |
| `latestOfMany`, `ofMany`, `withDefault`, `chaperone`, `touches` | `OfMany`, `WithDefault`, `Chaperone`, `Touches` |
| `with`, nested and constrained eager loading, `load` | `With(rel, scopes...)`, `orm.Load` |
| `has`, `whereHas`, `doesntHave`, `whereRelation`, `whereHasMorph`, `whereMorphedTo` | `HasCount`, `WhereHas`, `WhereDoesntHave`, `WhereRelation`, `Rel.HasMorph`, `Rel.Is` |
| `whereBelongsTo`, `whereAttachedTo` | `PostAuthor.Is(&u)`, `UserRoles.AttachedTo(&role)` |
| `withCount/Sum/Avg/Min/Max/Exists` | same names, into typed virtual columns |
| `$user->posts()->...`, `create`, `save`, `associate` | `UserPosts.Of(&u)...`, `.Create`, `.Save`, `.Associate`, `.Dissociate` |
| `attach`, `detach`, `sync`, `toggle`, `updateExistingPivot` | same names |
| Casts (array/json, dates, enums, custom) | `orm.JSON[T]`, `time.Time`, typed string enums, any `sql.Scanner` / `driver.Valuer` |
| Model factories | `orm.NewFactory(...).Count(n).State(...).Sequence(...).Create(ctx)` |
| Prunable / MassPrunable | `Prune(ctx, chunk)` / `Delete` |
| `DB::transaction`, nested savepoints, `afterCommit` | `orm.Transaction` (nests), `orm.AfterCommit` |
| `DB::listen`, `toSql`, `toRawSql` | `orm.Listen`, `ToSQL`, `ToRawSQL` |
| Connections, `Model::on()` | `orm.Open` / `AddConnection`, `connection=` directive, `Query.On(name)` |
| `Schema::create/table/drop/rename/hasTable/hasColumn(s)/hasIndex` | `schema.Create`, `Table`, `Drop`, `Rename`, `HasTable`, `HasColumns`, `HasIndex` |
| All Blueprint column types and modifiers | see the cookbook; generated, identity, charset, collation, `after`, `first`, `comment`, `useCurrent`, ... |
| Indexes (primary, unique, index, fullText, spatial), `renameIndex` | same names; SQLite alterations rebuild the table like Laravel |
| Foreign keys, `constrained`, `cascadeOnDelete`, `dropConstrainedForeignId` | same names |
| `getColumns`, `getIndexes`, `getForeignKeys`, `getTables` | same names |
| `migrate`, `rollback`, `reset`, `refresh`, `fresh`, `status`, `--step`, `--pretend`, `make:migration`, `db:wipe` | `schema.Run` commands and `Migrator` methods |

## What Go doesn't allow

A few Eloquent features depend on PHP runtime features Go deliberately lacks.
Each has a typed equivalent:

- **Magic properties and lazy loading** (`$user->posts`). Relations are loaded
  explicitly with `With` or `orm.Load`, and queried with `UserPosts.Of(&u)`.
- **Dynamic methods** (`whereEmail('x')`, `__call` macros). Use
  `Where(Users.Email.Eq("x"))` and plain Go functions as scopes.
- **Dynamic attributes** (`$user->posts_count`, accessors appended at
  runtime). Declare a `virtual` column for selected extras, and write
  accessors and mutators as ordinary Go methods.
- **Mass assignment / `$fillable`**. Unnecessary, since models are structs and
  updates are typed `Column.Set(v)` assignments.
- **Inheriting static methods** (`User::find()` via `extends Model`). The
  generated schema value plays that role: `Users.Find(ctx, id)`.
- **Variadic generic tuples** (`select(a, b, c)` into an anonymous row). Select into a
  struct's columns, use `PluckMap` for pairs, or add virtual columns.
- **Interfaces with generic methods**. Relation behaviour is reached through
  `Query` methods, not through an interface you could implement yourself.

Engine limitations are surfaced, not hidden. SQLite has no full-text indexes,
so `WhereFullText` falls back to `LIKE`, and its pragma-based foreign-key
toggling has no effect inside a transaction.

## Testing

```sh
make test                      # SQLite, no setup needed
docker compose up -d --wait    # Postgres 17, MySQL 8.4, MariaDB 11.4
make test-all                  # SQLite + all three servers
```

CI (`.github/workflows/test.yml`) runs the same matrix.
