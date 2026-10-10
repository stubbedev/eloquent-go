package orm

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// DuckDB: embedded analytical engine, Postgres-flavoured SQL with ?
// placeholders. Reads CSV/Parquet/JSON files straight from the builder, so
// it doubles as the flat-file backend (orm.From("read_csv('data.csv')")).
// ---------------------------------------------------------------------------

type duckdbDialect struct{ sqliteDialect }

var DuckDB Dialect = duckdbDialect{}

func (duckdbDialect) Name() string { return "duckdb" }

func (duckdbDialect) Bool(v bool) string {
	return map[bool]string{true: "true", false: "false"}[v]
}

func (duckdbDialect) DatePart(b *SQL, part string, e AnyExpr) {
	b.Write("date_part(")
	b.Arg(part)
	b.Write(", ")
	e.build(b)
	b.Write(")")
}

func (duckdbDialect) JSONExtract(b *SQL, e AnyExpr, path []string) {
	e.build(b)
	b.Write(" ->> ")
	b.Arg(jsonPath(path))
}

func (duckdbDialect) JSONContains(b *SQL, e AnyExpr, v any) {
	b.Write("json_contains(")
	e.build(b)
	b.Write(", CAST(")
	b.Arg(jsonOf(v))
	b.Write(" AS JSON))")
}

func (duckdbDialect) JSONOverlaps(b *SQL, e AnyExpr, v any) {
	b.Write("json_contains(")
	e.build(b)
	b.Write(", CAST(")
	b.Arg(jsonOf(v))
	b.Write(" AS JSON))")
}

func (duckdbDialect) JSONLength(b *SQL, e AnyExpr) {
	b.Write("json_array_length(")
	e.build(b)
	b.Write(")")
}

func (duckdbDialect) JSONHasKey(b *SQL, e AnyExpr, path []string) {
	e.build(b)
	b.Write(" -> ")
	b.Arg(jsonPath(path))
	b.Write(" IS NOT NULL")
}

func (duckdbDialect) FullText(b *SQL, exprs []AnyExpr, term string) {
	likeAll(b, exprs, term)
}

func (duckdbDialect) Truncate(table string) []string {
	return []string{"DELETE FROM " + doubleQuote(table)}
}

func (duckdbDialect) VectorDistance(b *SQL, e AnyExpr, arg any, m VectorMetric) {
	fs := floatsOf(arg)
	if fs == nil {
		b.Write("array_distance(")
		e.build(b)
		b.Write(", ")
		b.Arg(arg)
		b.Write(")")
		return
	}
	// array_distance needs both sides as the same fixed-size FLOAT[N]; a
	// bound parameter cannot match, so the query vector's components are
	// inlined as a literal array (our own floats, not user text).
	n := strconv.Itoa(len(fs))
	switch m {
	case Cosine:
		b.Write("1 - array_cosine_similarity(CAST(")
	case InnerProduct:
		b.Write("-array_inner_product(CAST(")
	default:
		b.Write("array_distance(CAST(")
	}
	e.build(b)
	b.Write(" AS FLOAT[" + n + "]), CAST([")
	for i, f := range fs {
		if i > 0 {
			b.Write(", ")
		}
		b.Write(strconv.FormatFloat(f, 'g', -1, 64))
	}
	b.Write("] AS FLOAT[" + n + "]))")
}

func floatsOf(arg any) []float64 {
	if vv, ok := arg.(interface{ Floats() []float64 }); ok {
		return vv.Floats()
	}
	return nil
}

// ---------------------------------------------------------------------------
// ClickHouse: OLAP with a real SQL surface but different physics — no
// transactions, mutations instead of UPDATE, MergeTree engines. Queries,
// inserts and deletes translate; updates run as ALTER TABLE ... UPDATE and
// auto-increment keys are not available (use explicit or UUID keys).
// ---------------------------------------------------------------------------

type clickhouseDialect struct{ sqliteDialect }

var ClickHouse Dialect = clickhouseDialect{}

func (clickhouseDialect) Name() string { return "clickhouse" }

func (clickhouseDialect) Quote(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}

func (clickhouseDialect) Returning() bool { return false }

func (clickhouseDialect) AutoKeys() bool { return false }

func (clickhouseDialect) Lock(LockMode) string { return "" }

func (clickhouseDialect) Random() string { return "rand()" }

func (clickhouseDialect) Limit(b *SQL, l, o int) {
	if l > 0 {
		b.Write(" LIMIT ")
		b.Write(intText(l))
	} else if o > 0 {
		b.Write(" LIMIT 18446744073709551615")
	}
	if o > 0 {
		b.Write(" OFFSET ")
		b.Write(intText(o))
	}
}

func (clickhouseDialect) DatePart(b *SQL, part string, e AnyExpr) {
	switch part {
	case "date":
		b.Write("CAST(toDate(")
		e.build(b)
		b.Write(") AS String)")
	case "time":
		b.Write("formatDateTime(")
		e.build(b)
		b.Write(", '%H:%i:%S')")
	case "year":
		b.Write("toYear(")
		e.build(b)
		b.Write(")")
	case "month":
		b.Write("toMonth(")
		e.build(b)
		b.Write(")")
	case "day":
		b.Write("toDayOfMonth(")
		e.build(b)
		b.Write(")")
	}
}

func (clickhouseDialect) JSONExtract(b *SQL, e AnyExpr, path []string) {
	b.Write("JSONExtractRaw(")
	e.build(b)
	for _, p := range path {
		b.Write(", ")
		b.Arg(p)
	}
	b.Write(")")
}

func (clickhouseDialect) JSONContains(b *SQL, e AnyExpr, v any) {
	b.Write("has(CAST(")
	e.build(b)
	b.Write(" AS Array(String)), toJSONString(")
	b.Arg(v)
	b.Write("))")
}

func (clickhouseDialect) JSONOverlaps(b *SQL, e AnyExpr, v any) {
	b.Write("length(arrayIntersect(CAST(")
	e.build(b)
	b.Write(" AS Array(String)), CAST(")
	b.Arg(jsonOf(sliceOf(v)))
	b.Write(" AS Array(String)))) > 0")
}

func (clickhouseDialect) JSONLength(b *SQL, e AnyExpr) {
	b.Write("JSONLength(")
	e.build(b)
	b.Write(")")
}

func (clickhouseDialect) JSONHasKey(b *SQL, e AnyExpr, path []string) {
	b.Write("JSONHas(")
	e.build(b)
	for _, p := range path {
		b.Write(", ")
		b.Arg(p)
	}
	b.Write(")")
}

func (clickhouseDialect) FullText(b *SQL, exprs []AnyExpr, term string) {
	b.Write("multiSearchAnyCaseInsensitive(array(")
	for i, e := range exprs {
		if i > 0 {
			b.Write(", ")
		}
		e.build(b)
	}
	b.Write("), array(")
	b.Arg(term)
	b.Write(")) > 0")
}

func (clickhouseDialect) Truncate(table string) []string {
	return []string{"TRUNCATE TABLE " + clickhouseDialect{}.Quote(table)}
}

func (clickhouseDialect) UpdatePrefix(b *SQL, table string) {
	b.plain = true // mutations reject qualified columns in WHERE
	b.Write("ALTER TABLE ")
	b.Ident(table)
	b.Write(" UPDATE")
}

func (clickhouseDialect) DeletePrefix(b *SQL, table string) {
	b.plain = true
	b.Write("DELETE FROM ")
	b.Ident(table)
}

// mutationSuffix makes ClickHouse mutations synchronous and row-counted.
func (clickhouseDialect) mutationSuffix(b *SQL) {
	b.Write(" SETTINGS mutations_sync = 2")
}

func (clickhouseDialect) VectorDistance(b *SQL, e AnyExpr, arg any, m VectorMetric) {
	b.Write("sqrt(")
	b.Write(metricName(m))
	b.Write("(")
	e.build(b)
	b.Write(", ")
	b.Arg(arg)
	b.Write("))")
}

func metricName(m VectorMetric) string {
	if m == Cosine {
		return "cosineDistance"
	}
	return "L2Distance"
}

func intText(n int) string { return strconv.Itoa(n) }

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func sliceOf(v any) []any {
	if vs, ok := v.([]any); ok {
		return vs
	}
	return []any{v}
}

func likeAll(b *SQL, exprs []AnyExpr, term string) {
	for i, e := range exprs {
		if i > 0 {
			b.Write(" OR ")
		}
		e.build(b)
		b.Write(" LIKE ")
		b.Arg("%" + term + "%")
	}
}
