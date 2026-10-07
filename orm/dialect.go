package orm

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Dialect is everything that differs between database engines. Pick one per
// connection; the same query then renders for that engine.
type Dialect interface {
	Name() string
	Placeholder(n int) string
	Quote(ident string) string
	Bool(v bool) string
	// Returning reports whether INSERT ... RETURNING is supported; if not,
	// LastInsertId is used for auto-incrementing keys.
	Returning() bool
	// InsertVerb is "INSERT" or e.g. MySQL's "INSERT IGNORE" when ignore is set.
	InsertVerb(ignore bool) string
	// OnConflict writes the upsert / insert-or-ignore clause.
	OnConflict(b *SQL, ignore bool, uniqueBy, update []string)
	Lock(mode LockMode) string
	// Limit writes LIMIT/OFFSET; zero means absent.
	Limit(b *SQL, limit, offset int)
	Random() string
	// DatePart extracts "date", "time", "year", "month" or "day" from e.
	DatePart(b *SQL, part string, e AnyExpr)
	JSONExtract(b *SQL, e AnyExpr, path []string)
	JSONContains(b *SQL, e AnyExpr, v any)
	JSONLength(b *SQL, e AnyExpr)
	JSONHasKey(b *SQL, e AnyExpr, path []string)
	// FullText writes a full text match of term against exprs (whereFullText).
	FullText(b *SQL, exprs []AnyExpr, term string)
	// Truncate empties a table and resets its auto-increment counter.
	Truncate(table string) []string
}

// LockMode is a pessimistic row lock.
type LockMode int

const (
	NoLock LockMode = iota
	ForUpdate
	ForShare
)

var (
	SQLite   Dialect = sqliteDialect{}
	Postgres Dialect = postgresDialect{}
	MySQL    Dialect = mysqlDialect{}
)

// DialectFor maps a database/sql driver name to a dialect.
func DialectFor(driver string) (Dialect, bool) {
	switch driver {
	case "sqlite", "sqlite3", "libsql":
		return SQLite, true
	case "postgres", "pgx", "pq":
		return Postgres, true
	case "mysql", "mariadb":
		return MySQL, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------

type sqliteDialect struct{}

func (sqliteDialect) Name() string                 { return "sqlite" }
func (sqliteDialect) Placeholder(int) string       { return "?" }
func (sqliteDialect) Quote(id string) string       { return doubleQuote(id) }
func (sqliteDialect) Bool(v bool) string           { return map[bool]string{true: "1", false: "0"}[v] }
func (sqliteDialect) Returning() bool              { return true }
func (sqliteDialect) InsertVerb(bool) string       { return "INSERT" }
func (sqliteDialect) Lock(LockMode) string         { return "" } // SQLite locks the whole database
func (sqliteDialect) Random() string               { return "RANDOM()" }
func (sqliteDialect) Limit(b *SQL, l, o int)       { limit(b, l, o, "-1") }
func (sqliteDialect) JSONLength(b *SQL, e AnyExpr) { fn(b, "json_array_length", e) }

func (sqliteDialect) OnConflict(b *SQL, ignore bool, uniqueBy, update []string) {
	onConflict(b, sqliteDialect{}, ignore, uniqueBy, update)
}

func (sqliteDialect) DatePart(b *SQL, part string, e AnyExpr) {
	switch part {
	case "date", "time":
		fn(b, part, e)
	default:
		f := map[string]string{"year": "%Y", "month": "%m", "day": "%d"}[part]
		b.Write("CAST(strftime('", f, "', ")
		e.build(b)
		b.Write(") AS INTEGER)")
	}
}

func (sqliteDialect) JSONExtract(b *SQL, e AnyExpr, path []string) {
	b.Write("json_extract(")
	e.build(b)
	b.Write(", ")
	b.Arg(jsonPath(path))
	b.Write(")")
}

func (sqliteDialect) JSONHasKey(b *SQL, e AnyExpr, path []string) {
	b.Write("json_type(")
	e.build(b)
	b.Write(", ")
	b.Arg(jsonPath(path))
	b.Write(") IS NOT NULL")
}

// FullText on SQLite falls back to case-insensitive LIKE on each column;
// real full text search there needs an FTS5 virtual table.
func (sqliteDialect) FullText(b *SQL, exprs []AnyExpr, term string) {
	b.Write("(")
	for i, e := range exprs {
		if i > 0 {
			b.Write(" OR ")
		}
		e.build(b)
		b.Write(" LIKE ")
		b.Arg("%" + term + "%")
	}
	b.Write(")")
}

func (d sqliteDialect) Truncate(table string) []string {
	return []string{"DELETE FROM " + d.Quote(table), "DELETE FROM sqlite_sequence WHERE name = " + literal(d, table)}
}

// JSONContains propagates NULL for a NULL document, like Postgres and MySQL,
// so negating it doesn't match rows without JSON.
func (sqliteDialect) JSONContains(b *SQL, e AnyExpr, v any) {
	b.Write("CASE WHEN ")
	e.build(b)
	b.Write(" IS NULL THEN NULL ELSE EXISTS (SELECT 1 FROM json_each(")
	e.build(b)
	b.Write(") WHERE value = ")
	b.Arg(v)
	b.Write(") END")
}

// ---------------------------------------------------------------------------

type postgresDialect struct{}

func (postgresDialect) Name() string             { return "postgres" }
func (postgresDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (postgresDialect) Quote(id string) string   { return doubleQuote(id) }
func (postgresDialect) Bool(v bool) string       { return strings.ToUpper(strconv.FormatBool(v)) }
func (postgresDialect) Returning() bool          { return true }
func (postgresDialect) InsertVerb(bool) string   { return "INSERT" }
func (postgresDialect) Random() string           { return "RANDOM()" }
func (postgresDialect) Limit(b *SQL, l, o int)   { limit(b, l, o, "") }

func (postgresDialect) Lock(m LockMode) string {
	return map[LockMode]string{ForUpdate: " FOR UPDATE", ForShare: " FOR SHARE"}[m]
}

func (postgresDialect) OnConflict(b *SQL, ignore bool, uniqueBy, update []string) {
	onConflict(b, postgresDialect{}, ignore, uniqueBy, update)
}

func (postgresDialect) DatePart(b *SQL, part string, e AnyExpr) {
	switch part {
	case "date", "time":
		b.Write("CAST(")
		e.build(b)
		b.Write(" AS ", strings.ToUpper(part), ")")
	default:
		b.Write("CAST(EXTRACT(", strings.ToUpper(part), " FROM ")
		e.build(b)
		b.Write(") AS INTEGER)")
	}
}

func (postgresDialect) JSONExtract(b *SQL, e AnyExpr, path []string) {
	b.Write("(")
	e.build(b)
	b.Write(")::jsonb #>> ")
	b.Arg("{" + strings.Join(path, ",") + "}")
}

func (postgresDialect) JSONContains(b *SQL, e AnyExpr, v any) {
	enc, _ := json.Marshal([]any{v})
	b.Write("(")
	e.build(b)
	b.Write(")::jsonb @> ")
	b.Arg(string(enc))
	b.Write("::jsonb")
}

func (postgresDialect) JSONHasKey(b *SQL, e AnyExpr, path []string) {
	b.Write("(")
	e.build(b)
	b.Write(")::jsonb #> ")
	b.Arg("{" + strings.Join(path, ",") + "}")
	b.Write(" IS NOT NULL")
}

// FullText matches the expression index the schema grammar creates for
// FullText(...) columns, so Postgres can use it.
func (postgresDialect) FullText(b *SQL, exprs []AnyExpr, term string) {
	b.Write("(")
	for i, e := range exprs {
		if i > 0 {
			b.Write(" || ")
		}
		b.Write("to_tsvector('english', ")
		e.build(b)
		b.Write(")")
	}
	b.Write(") @@ plainto_tsquery('english', ")
	b.Arg(term)
	b.Write(")")
}

func (d postgresDialect) Truncate(table string) []string {
	return []string{"TRUNCATE TABLE " + d.Quote(table) + " RESTART IDENTITY CASCADE"}
}

func (postgresDialect) JSONLength(b *SQL, e AnyExpr) {
	b.Write("jsonb_array_length((")
	e.build(b)
	b.Write(")::jsonb)")
}

// ---------------------------------------------------------------------------

type mysqlDialect struct{}

func (mysqlDialect) Name() string           { return "mysql" }
func (mysqlDialect) Placeholder(int) string { return "?" }
func (mysqlDialect) Quote(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}
func (mysqlDialect) Bool(v bool) string     { return map[bool]string{true: "1", false: "0"}[v] }
func (mysqlDialect) Returning() bool        { return false }
func (mysqlDialect) Random() string         { return "RAND()" }
func (mysqlDialect) Limit(b *SQL, l, o int) { limit(b, l, o, "18446744073709551615") }

func (mysqlDialect) InsertVerb(ignore bool) string {
	if ignore {
		return "INSERT IGNORE"
	}
	return "INSERT"
}

func (mysqlDialect) Lock(m LockMode) string {
	return map[LockMode]string{ForUpdate: " FOR UPDATE", ForShare: " FOR SHARE"}[m]
}

func (mysqlDialect) OnConflict(b *SQL, ignore bool, _, update []string) {
	if ignore || len(update) == 0 {
		return // handled by INSERT IGNORE
	}
	b.Write(" ON DUPLICATE KEY UPDATE ")
	for i, c := range update {
		if i > 0 {
			b.Write(", ")
		}
		b.Ident(c)
		b.Write(" = VALUES(")
		b.Ident(c)
		b.Write(")")
	}
}

func (mysqlDialect) DatePart(b *SQL, part string, e AnyExpr) { fn(b, strings.ToUpper(part), e) }

func (mysqlDialect) JSONExtract(b *SQL, e AnyExpr, path []string) {
	b.Write("JSON_UNQUOTE(JSON_EXTRACT(")
	e.build(b)
	b.Write(", ")
	b.Arg(jsonPath(path))
	b.Write("))")
}

func (mysqlDialect) JSONContains(b *SQL, e AnyExpr, v any) {
	enc, _ := json.Marshal(v)
	b.Write("JSON_CONTAINS(")
	e.build(b)
	b.Write(", ")
	b.Arg(string(enc))
	b.Write(")")
}

func (mysqlDialect) JSONLength(b *SQL, e AnyExpr) { fn(b, "JSON_LENGTH", e) }

func (mysqlDialect) JSONHasKey(b *SQL, e AnyExpr, path []string) {
	b.Write("JSON_CONTAINS_PATH(")
	e.build(b)
	b.Write(", 'one', ")
	b.Arg(jsonPath(path))
	b.Write(")")
}

func (mysqlDialect) FullText(b *SQL, exprs []AnyExpr, term string) {
	b.Write("MATCH (")
	for i, e := range exprs {
		if i > 0 {
			b.Write(", ")
		}
		e.build(b)
	}
	b.Write(") AGAINST (")
	b.Arg(term)
	b.Write(" IN NATURAL LANGUAGE MODE)")
}

func (d mysqlDialect) Truncate(table string) []string {
	return []string{"TRUNCATE TABLE " + d.Quote(table)}
}

// ---------------------------------------------------------------------------

func onConflict(b *SQL, d Dialect, ignore bool, uniqueBy, update []string) {
	b.Write(" ON CONFLICT")
	if len(uniqueBy) > 0 {
		b.Write(" (")
		for i, c := range uniqueBy {
			if i > 0 {
				b.Write(", ")
			}
			b.Ident(c)
		}
		b.Write(")")
	}
	if ignore || len(update) == 0 {
		b.Write(" DO NOTHING")
		return
	}
	b.Write(" DO UPDATE SET ")
	for i, c := range update {
		if i > 0 {
			b.Write(", ")
		}
		b.Ident(c)
		b.Write(" = excluded.")
		b.Ident(c)
	}
}

// limit writes LIMIT/OFFSET; unbounded is the LIMIT required before a bare
// OFFSET ("" if the dialect allows OFFSET alone).
func limit(b *SQL, l, o int, unbounded string) {
	switch {
	case l > 0:
		b.Write(" LIMIT ", strconv.Itoa(l))
	case o > 0 && unbounded != "":
		b.Write(" LIMIT ", unbounded)
	}
	if o > 0 {
		b.Write(" OFFSET ", strconv.Itoa(o))
	}
}

func fn(b *SQL, name string, e AnyExpr) {
	b.Write(name, "(")
	e.build(b)
	b.Write(")")
}

func doubleQuote(id string) string { return `"` + strings.ReplaceAll(id, `"`, `""`) + `"` }

func jsonPath(path []string) string { return "$." + strings.Join(path, ".") }
