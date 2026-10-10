package schema

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Column types understood by every grammar.
const (
	TypeString        = "string"
	TypeChar          = "char"
	TypeTinyText      = "tinyText"
	TypeText          = "text"
	TypeMediumText    = "mediumText"
	TypeLongText      = "longText"
	TypeTinyInteger   = "tinyInteger"
	TypeSmallInteger  = "smallInteger"
	TypeMediumInteger = "mediumInteger"
	TypeInteger       = "integer"
	TypeBigInteger    = "bigInteger"
	TypeBoolean       = "boolean"
	TypeDecimal       = "decimal"
	TypeFloat         = "float"
	TypeDouble        = "double"
	TypeDate          = "date"
	TypeTime          = "time"
	TypeTimeTz        = "timeTz"
	TypeDateTime      = "dateTime"
	TypeDateTimeTz    = "dateTimeTz"
	TypeTimestamp     = "timestamp"
	TypeTimestampTz   = "timestampTz"
	TypeYear          = "year"
	TypeJSON          = "json"
	TypeJSONB         = "jsonb"
	TypeBinary        = "binary"
	TypeUUID          = "uuid"
	TypeULID          = "ulid"
	TypeIPAddress     = "ipAddress"
	TypeMACAddress    = "macAddress"
	TypeEnum          = "enum"
	TypeSet           = "set"
	TypeGeometry      = "geometry"
	TypeGeography     = "geography"
	TypeVector        = "vector"
)

// errRebuild signals that SQLite needs a table rebuild for an alteration
// it cannot express with ALTER TABLE (as Laravel does).
var errRebuild = errors.New("schema: sqlite table rebuild required")

// grammar compiles blueprints for one database engine.
type grammar struct{ kind string } // "sqlite", "postgres", "mysql", "duckdb", "clickhouse"

func grammarFor(dialect string) (grammar, error) {
	switch dialect {
	case "sqlite", "postgres", "mysql", "duckdb", "clickhouse":
		return grammar{dialect}, nil
	}
	return grammar{}, fmt.Errorf("schema: no grammar for dialect %q", dialect)
}

func (g grammar) pick(sqlite, postgres, mysql string) string {
	if v, ok := map[string]string{"sqlite": sqlite, "postgres": postgres, "mysql": mysql}[g.kind]; ok {
		return v
	}
	// DuckDB is Postgres-flavoured; ClickHouse is closest to the simple forms.
	if g.kind == "duckdb" {
		return postgres
	}
	return sqlite
}

func (g grammar) quote(id string) string {
	if g.kind == "mysql" || g.kind == "clickhouse" {
		return "`" + strings.ReplaceAll(id, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

func (g grammar) list(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = g.quote(id)
	}
	return strings.Join(q, ", ")
}

// transactionalDDL reports whether DDL can be rolled back (Laravel wraps
// migrations in a transaction on such engines).
func (g grammar) transactionalDDL() bool { return g.kind != "mysql" && g.kind != "clickhouse" }

func (g grammar) typeOf(c *Column) string {
	n := strconv.Itoa
	auto := c.IsAutoIncrement && c.Identity == ""
	switch g.kind {
	case "duckdb":
		return duckdbType(c)
	case "clickhouse":
		return clickhouseType(c)
	}
	switch c.Type {
	case TypeString:
		return g.pick("varchar", "varchar("+n(c.Length)+")", "varchar("+n(c.Length)+")")
	case TypeChar:
		return g.pick("varchar", "char("+n(c.Length)+")", "char("+n(c.Length)+")")
	case TypeTinyText:
		return g.pick("text", "varchar(255)", "tinytext")
	case TypeText:
		return "text"
	case TypeMediumText:
		return g.pick("text", "text", "mediumtext")
	case TypeLongText:
		return g.pick("text", "text", "longtext")
	case TypeTinyInteger:
		if auto {
			return g.pick("integer", "smallserial", "tinyint")
		}
		return g.pick("integer", "smallint", "tinyint")
	case TypeSmallInteger:
		if auto {
			return g.pick("integer", "smallserial", "smallint")
		}
		return g.pick("integer", "smallint", "smallint")
	case TypeMediumInteger:
		if auto {
			return g.pick("integer", "serial", "mediumint")
		}
		return g.pick("integer", "integer", "mediumint")
	case TypeInteger:
		if auto {
			return g.pick("integer", "serial", "int")
		}
		return g.pick("integer", "integer", "int")
	case TypeBigInteger:
		if auto {
			return g.pick("integer", "bigserial", "bigint")
		}
		return g.pick("integer", "bigint", "bigint")
	case TypeBoolean:
		return g.pick("tinyint(1)", "boolean", "tinyint(1)")
	case TypeDecimal:
		return g.pick("numeric", "decimal", "decimal") + "(" + n(c.Precision) + ", " + n(c.Scale) + ")"
	case TypeFloat:
		return g.pick("float", "float("+n(c.Precision)+")", "float("+n(c.Precision)+")")
	case TypeDouble:
		return g.pick("double", "double precision", "double")
	case TypeDate:
		return "date"
	case TypeTime:
		return g.pick("time", "time(0) without time zone", "time")
	case TypeTimeTz:
		return g.pick("time", "time(0) with time zone", "time")
	case TypeDateTime:
		return g.pick("datetime", "timestamp(0) without time zone", "datetime")
	case TypeDateTimeTz:
		return g.pick("datetime", "timestamp(0) with time zone", "datetime")
	case TypeTimestamp:
		return g.pick("datetime", "timestamp(0) without time zone", "timestamp")
	case TypeTimestampTz:
		return g.pick("datetime", "timestamp(0) with time zone", "timestamp")
	case TypeYear:
		return g.pick("integer", "integer", "year")
	case TypeJSON:
		return g.pick("text", "json", "json")
	case TypeJSONB:
		return g.pick("text", "jsonb", "json")
	case TypeBinary:
		return g.pick("blob", "bytea", "blob")
	case TypeUUID:
		return g.pick("varchar", "uuid", "char(36)")
	case TypeULID:
		return g.pick("varchar", "char(26)", "char(26)")
	case TypeIPAddress:
		return g.pick("varchar", "inet", "varchar(45)")
	case TypeMACAddress:
		return g.pick("varchar", "macaddr", "varchar(17)")
	case TypeEnum:
		if g.kind == "mysql" {
			return "enum(" + g.literals(c.Allowed) + ")"
		}
		return g.pick("varchar", "varchar(255)", "")
	case TypeSet:
		if g.kind == "mysql" {
			return "set(" + g.literals(c.Allowed) + ")"
		}
		return g.pick("varchar", "varchar(255)", "")
	case TypeGeometry, TypeGeography:
		sub := cmpOr(strings.ToLower(c.Subtype), "geometry")
		if g.kind == "postgres" {
			kind := map[string]string{TypeGeometry: "geometry", TypeGeography: "geography"}[c.Type]
			if c.SRID > 0 {
				return kind + "(" + sub + ", " + n(c.SRID) + ")"
			}
			return kind + "(" + sub + ")"
		}
		if g.kind == "mysql" && c.SRID > 0 {
			return sub + " srid " + n(c.SRID)
		}
		return sub
	case TypeVector:
		return g.pick("blob", "vector("+n(c.Length)+")", "vector("+n(c.Length)+")")
	}
	panic("schema: unknown column type " + c.Type)
}

func (g grammar) literals(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = g.literal(v)
	}
	return strings.Join(q, ", ")
}

func (g grammar) literal(v any) string {
	switch v := v.(type) {
	case Expr:
		return string(v)
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	case bool:
		switch g.kind {
		case "postgres", "duckdb", "clickhouse":
			return strings.ToUpper(strconv.FormatBool(v))
		}
		return map[bool]string{true: "1", false: "0"}[v]
	case time.Time:
		return "'" + v.Format("2006-01-02 15:04:05") + "'"
	case nil:
		return "NULL"
	}
	return fmt.Sprint(v)
}

// column renders a full column definition.
func (g grammar) column(c *Column) string {
	var b strings.Builder
	w := func(s ...string) {
		for _, p := range s {
			b.WriteString(p)
		}
	}
	w(g.quote(c.Name), " ", g.typeOf(c))
	switch g.kind {
	case "mysql":
		if c.IsUnsigned && isNumeric(c.Type) {
			w(" unsigned")
		}
		if c.CharsetName != "" {
			w(" CHARACTER SET ", c.CharsetName)
		}
		if c.CollationName != "" {
			w(" COLLATE ", g.literal(c.CollationName))
		}
	case "postgres":
		if c.CollationName != "" {
			w(" COLLATE ", g.quote(c.CollationName))
		}
	case "sqlite":
		if c.IsAutoIncrement {
			w(" PRIMARY KEY AUTOINCREMENT")
		}
		if c.CollationName != "" {
			w(" COLLATE ", c.CollationName)
		}
	}
	if c.StoredExpr != "" || c.VirtualExpr != "" {
		expr, kind := c.StoredExpr, "STORED"
		if expr == "" {
			expr, kind = c.VirtualExpr, "VIRTUAL"
		}
		w(g.pick(" GENERATED ALWAYS AS (", " GENERATED ALWAYS AS (", " AS ("), expr, ") ", kind)
	}
	// Generated columns take their nullability from the expression (MariaDB
	// rejects an explicit NOT NULL), as in Laravel's grammars.
	if c.StoredExpr == "" && c.VirtualExpr == "" && g.kind != "clickhouse" {
		// ClickHouse carries nullability in Nullable(type) itself.
		if c.IsNullable {
			w(" NULL")
		} else {
			w(" NOT NULL")
		}
	}
	if c.IsInvisible && g.kind == "mysql" {
		w(" INVISIBLE")
	}
	if c.HasDefault {
		w(" DEFAULT ", g.literal(c.DefaultValue))
	}
	if c.OnUpdateNow && g.kind == "mysql" {
		w(" ON UPDATE CURRENT_TIMESTAMP")
	}
	if (c.Type == TypeEnum) && g.kind != "mysql" && g.kind != "clickhouse" {
		w(" CHECK (", g.quote(c.Name), " IN (", g.literals(c.Allowed), "))")
	}
	if c.Identity != "" && g.kind == "postgres" {
		w(" GENERATED ", c.Identity, " AS IDENTITY")
	}
	if c.IsAutoIncrement {
		switch g.kind {
		case "postgres":
			w(" PRIMARY KEY")
		case "mysql":
			w(" AUTO_INCREMENT PRIMARY KEY")
		case "duckdb":
			w(" PRIMARY KEY DEFAULT nextval('", c.bp.table+"_"+c.Name+"_seq')")
		}
	}
	if g.kind == "mysql" {
		if c.CommentText != "" {
			w(" COMMENT ", g.literal(c.CommentText))
		}
		if c.IsFirst {
			w(" FIRST")
		} else if c.AfterColumn != "" {
			w(" AFTER ", g.quote(c.AfterColumn))
		}
	}
	return b.String()
}

func isNumeric(t string) bool {
	switch t {
	case TypeTinyInteger, TypeSmallInteger, TypeMediumInteger, TypeInteger, TypeBigInteger, TypeDecimal, TypeFloat, TypeDouble:
		return true
	}
	return false
}

func (g grammar) compileCreate(bp *Blueprint) ([]string, error) {
	var defs, after []string
	for _, c := range bp.columns {
		defs = append(defs, g.column(c))
	}
	for _, cmd := range bp.commands {
		switch cmd.kind {
		case "primary":
			if g.kind == "sqlite" {
				defs = append(defs, "PRIMARY KEY ("+g.list(cmd.columns)+")")
			} else {
				defs = append(defs, "CONSTRAINT "+g.quote(g.primaryName(bp.table, cmd.index))+" PRIMARY KEY ("+g.list(cmd.columns)+")")
			}
		case "foreign":
			defs = append(defs, g.foreign(cmd.foreign))
		case "unique", "index", "fullText", "spatial", "hnsw":
			s, err := g.createIndex(bp.table, cmd)
			if err != nil {
				return nil, err
			}
			after = append(after, s)
		default:
			return nil, fmt.Errorf("schema: %s is not valid when creating table %s", cmd.kind, bp.table)
		}
	}
	var head strings.Builder
	head.WriteString("CREATE ")
	if bp.temporary {
		head.WriteString("TEMPORARY ")
	}
	head.WriteString("TABLE ")
	if bp.ifNot {
		head.WriteString("IF NOT EXISTS ")
	}
	head.WriteString(g.quote(bp.table) + " (" + strings.Join(defs, ", ") + ")")
	switch g.kind {
	case "mysql":
		if bp.engine != "" {
			head.WriteString(" ENGINE = " + bp.engine)
		}
		if bp.charset != "" {
			head.WriteString(" DEFAULT CHARACTER SET = " + bp.charset)
		}
		if bp.collation != "" {
			head.WriteString(" COLLATE = " + g.literal(bp.collation))
		}
		if bp.comment != "" {
			head.WriteString(" COMMENT = " + g.literal(bp.comment))
		}
	case "clickhouse":
		// Every ClickHouse table needs a table engine; MergeTree is the
		// default, overridable with Blueprint engine option.
		engine := cmpOr(bp.engine, "MergeTree")
		head.WriteString(" ENGINE = " + engine + " ORDER BY tuple()")
	}
	stmts := append([]string{head.String()}, after...)
	stmts = append(stmts, g.startingValues(bp)...)
	stmts = append(stmts, g.comments(bp)...)
	if g.kind == "duckdb" {
		// DuckDB has no serial types; auto-increment columns draw from a
		// per-table sequence the column's DEFAULT references.
		var seqs []string
		for _, c := range bp.columns {
			if c.IsAutoIncrement {
				seqs = append(seqs, "CREATE SEQUENCE IF NOT EXISTS "+g.quote(bp.table+"_"+c.Name+"_seq"))
			}
		}
		if len(seqs) > 0 {
			stmts = append(seqs, stmts...)
		}
	}
	return stmts, nil
}

func (g grammar) primaryName(table string, idx *Index) string {
	if g.kind == "postgres" && (idx == nil || idx.IndexName == indexName(table, "primary", idx.Columns)) {
		return table + "_pkey"
	}
	return idx.IndexName
}

func (g grammar) startingValues(bp *Blueprint) []string {
	var stmts []string
	for _, c := range bp.columns {
		if !c.IsAutoIncrement || c.StartingValue == 0 {
			continue
		}
		v := strconv.Itoa(c.StartingValue)
		switch g.kind {
		case "mysql":
			stmts = append(stmts, "ALTER TABLE "+g.quote(bp.table)+" AUTO_INCREMENT = "+v)
		case "postgres":
			stmts = append(stmts, "ALTER SEQUENCE "+g.quote(bp.table+"_"+c.Name+"_seq")+" RESTART WITH "+v)
		case "sqlite":
			stmts = append(stmts, "INSERT INTO sqlite_sequence (name, seq) VALUES ("+g.literal(bp.table)+", "+strconv.Itoa(c.StartingValue-1)+")")
		}
	}
	return stmts
}

// compileAlter compiles Schema::table(). On SQLite it returns errRebuild
// when an operation needs the table to be rebuilt.
func (g grammar) compileAlter(bp *Blueprint) ([]string, error) {
	t := g.quote(bp.table)
	var stmts []string
	for _, c := range bp.columns {
		if !c.Changed {
			stmts = append(stmts, "ALTER TABLE "+t+" ADD COLUMN "+g.column(c))
			continue
		}
		switch g.kind {
		case "mysql":
			stmts = append(stmts, "ALTER TABLE "+t+" MODIFY "+g.column(c))
		case "postgres":
			col := "ALTER TABLE " + t + " ALTER COLUMN " + g.quote(c.Name)
			stmts = append(stmts, col+" TYPE "+g.typeOf(c)+" USING "+g.quote(c.Name)+"::"+g.typeOf(c))
			stmts = append(stmts, col+map[bool]string{true: " DROP NOT NULL", false: " SET NOT NULL"}[c.IsNullable])
			if c.HasDefault {
				stmts = append(stmts, col+" SET DEFAULT "+g.literal(c.DefaultValue))
			} else {
				stmts = append(stmts, col+" DROP DEFAULT")
			}
		default:
			return nil, errRebuild
		}
	}
	for _, cmd := range bp.commands {
		switch cmd.kind {
		case "unique", "index", "fullText", "spatial", "hnsw":
			s, err := g.createIndex(bp.table, cmd)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, s)
		case "dropColumn":
			for _, c := range cmd.columns {
				stmts = append(stmts, "ALTER TABLE "+t+" DROP COLUMN "+g.quote(c))
			}
		case "renameColumn":
			stmts = append(stmts, "ALTER TABLE "+t+" RENAME COLUMN "+g.quote(cmd.from)+" TO "+g.quote(cmd.to))
		case "renameIndex":
			switch g.kind {
			case "mysql":
				stmts = append(stmts, "ALTER TABLE "+t+" RENAME INDEX "+g.quote(cmd.from)+" TO "+g.quote(cmd.to))
			case "postgres":
				stmts = append(stmts, "ALTER INDEX "+g.quote(cmd.from)+" RENAME TO "+g.quote(cmd.to))
			default:
				return nil, errRebuild
			}
		case "dropIndex":
			if g.kind == "mysql" {
				stmts = append(stmts, "ALTER TABLE "+t+" DROP INDEX "+g.quote(cmd.index.IndexName))
			} else {
				stmts = append(stmts, "DROP INDEX "+g.quote(cmd.index.IndexName))
			}
		case "foreign":
			if g.kind == "sqlite" {
				return nil, errRebuild
			}
			stmts = append(stmts, "ALTER TABLE "+t+" ADD "+g.foreign(cmd.foreign))
		case "dropForeign":
			switch g.kind {
			case "sqlite":
				return nil, errRebuild
			case "mysql":
				stmts = append(stmts, "ALTER TABLE "+t+" DROP FOREIGN KEY "+g.quote(cmd.index.IndexName))
			default:
				stmts = append(stmts, "ALTER TABLE "+t+" DROP CONSTRAINT "+g.quote(cmd.index.IndexName))
			}
		case "primary":
			if g.kind == "sqlite" {
				return nil, errRebuild
			}
			stmts = append(stmts, "ALTER TABLE "+t+" ADD CONSTRAINT "+g.quote(g.primaryName(bp.table, cmd.index))+" PRIMARY KEY ("+g.list(cmd.columns)+")")
		case "dropPrimary":
			switch g.kind {
			case "sqlite":
				return nil, errRebuild
			case "mysql":
				stmts = append(stmts, "ALTER TABLE "+t+" DROP PRIMARY KEY")
			default:
				stmts = append(stmts, "ALTER TABLE "+t+" DROP CONSTRAINT "+g.quote(bp.table+"_pkey"))
			}
		}
	}
	if bp.comment != "" {
		switch g.kind {
		case "mysql":
			stmts = append(stmts, "ALTER TABLE "+t+" COMMENT = "+g.literal(bp.comment))
		}
	}
	stmts = append(stmts, g.startingValues(bp)...)
	return append(stmts, g.comments(bp)...), nil
}

func (g grammar) foreign(f *Foreign) string {
	refCols := f.RefColumns
	if len(refCols) == 0 {
		refCols = []string{"id"}
	}
	s := "CONSTRAINT " + g.quote(f.Name) + " FOREIGN KEY (" + g.list(f.Columns) + ") REFERENCES " +
		g.quote(f.RefTable) + " (" + g.list(refCols) + ")"
	if f.OnDel != "" {
		s += " ON DELETE " + strings.ToUpper(f.OnDel)
	}
	if f.OnUpd != "" {
		s += " ON UPDATE " + strings.ToUpper(f.OnUpd)
	}
	if f.Defer && g.kind == "postgres" {
		s += " DEFERRABLE"
		if f.InitDefer {
			s += " INITIALLY DEFERRED"
		}
	}
	return s
}

func (g grammar) createIndex(table string, cmd *command) (string, error) {
	idx := cmd.index
	name, on := g.quote(idx.IndexName), " ON "+g.quote(table)
	cols := " (" + g.list(cmd.columns) + ")"
	switch cmd.kind {
	case "fullText":
		switch g.kind {
		case "mysql":
			return "CREATE FULLTEXT INDEX " + name + on + cols, nil
		case "postgres":
			lang := cmpOr(idx.Lang, "english")
			parts := make([]string, len(cmd.columns))
			for i, c := range cmd.columns {
				parts[i] = "to_tsvector(" + g.literal(lang) + ", " + g.quote(c) + ")"
			}
			return "CREATE INDEX " + name + on + " USING gin ((" + strings.Join(parts, " || ") + "))", nil
		}
		return "", fmt.Errorf("schema: full text indexes are not supported on %s", g.kind)
	case "spatial":
		switch g.kind {
		case "mysql":
			return "CREATE SPATIAL INDEX " + name + on + cols, nil
		case "postgres":
			return "CREATE INDEX " + name + on + " USING gist" + cols, nil
		}
		return "", fmt.Errorf("schema: spatial indexes are not supported on %s", g.kind)
	case "hnsw":
		if g.kind != "postgres" {
			return "", fmt.Errorf("schema: hnsw vector indexes need pgvector (not %s)", g.kind)
		}
		op := "vector_l2_ops"
		if idx.VectorOp != "" {
			op = idx.VectorOp
		}
		return "CREATE INDEX " + name + on + " USING hnsw (" + g.quote(cmd.columns[0]) + " " + op + ")", nil
	}
	unique := map[bool]string{true: "UNIQUE ", false: ""}[cmd.kind == "unique"]
	s := "CREATE " + unique + "INDEX " + name + on
	switch {
	case idx.Algo != "" && g.kind == "postgres":
		s += " USING " + idx.Algo + cols
	case idx.Algo != "" && g.kind == "mysql":
		s += cols + " USING " + strings.ToUpper(idx.Algo)
	default:
		s += cols
	}
	return s, nil
}

func (g grammar) comments(bp *Blueprint) []string {
	if g.kind != "postgres" {
		return nil
	}
	var stmts []string
	if bp.comment != "" {
		stmts = append(stmts, "COMMENT ON TABLE "+g.quote(bp.table)+" IS "+g.literal(bp.comment))
	}
	for _, c := range bp.columns {
		if c.CommentText != "" {
			stmts = append(stmts, "COMMENT ON COLUMN "+g.quote(bp.table)+"."+g.quote(c.Name)+" IS "+g.literal(c.CommentText))
		}
	}
	return stmts
}

func (g grammar) drop(table string, ifExists bool) string {
	s := "DROP TABLE "
	if ifExists {
		s += "IF EXISTS "
	}
	s += g.quote(table)
	if g.kind == "postgres" {
		s += " CASCADE"
	}
	return s
}

func (g grammar) rename(from, to string) string {
	if g.kind == "mysql" {
		return "RENAME TABLE " + g.quote(from) + " TO " + g.quote(to)
	}
	return "ALTER TABLE " + g.quote(from) + " RENAME TO " + g.quote(to)
}

// foreignKeyChecks toggles constraint enforcement (Schema::enable/disableForeignKeyConstraints).
func (g grammar) foreignKeyChecks(on bool) string {
	switch g.kind {
	case "sqlite":
		return "PRAGMA foreign_keys = " + map[bool]string{true: "ON", false: "OFF"}[on]
	case "mysql":
		return "SET FOREIGN_KEY_CHECKS = " + map[bool]string{true: "1", false: "0"}[on]
	case "postgres":
		return "SET CONSTRAINTS ALL " + map[bool]string{true: "IMMEDIATE", false: "DEFERRED"}[on]
	}
	return ""
}
