// Package schema defines tables and migrations, Laravel style:
//
//	schema.Create(ctx, "users", func(t *schema.Blueprint) {
//		t.ID()
//		t.ForeignID("country_id").Nullable().Constrained().NullOnDelete()
//		t.String("email").Unique()
//		t.Boolean("active").Default(true)
//		t.JSON("settings").Nullable()
//		t.Timestamps()
//		t.SoftDeletes()
//	})
//
// DDL is compiled by a grammar chosen from the connection's dialect, so the
// same blueprint produces SQLite, Postgres or MySQL statements.
package schema

import (
	"strings"

	"github.com/stubbedev/eloquent-go/orm"
)

// Blueprint collects column definitions and commands for one table.
type Blueprint struct {
	table     string
	create    bool
	ifNot     bool
	temporary bool
	engine    string
	charset   string
	collation string
	comment   string
	columns   []*Column
	commands  []*command
}

type command struct {
	kind    string // primary, unique, index, fullText, spatial, foreign, dropColumn, renameColumn, renameIndex, dropIndex, dropForeign, dropPrimary
	index   *Index
	columns []string
	from    string
	to      string
	foreign *Foreign
}

// Column is a column definition with chainable modifiers.
type Column struct {
	Name            string
	Type            string // see the Type constants in grammar.go
	Length          int
	Precision       int
	Scale           int
	Allowed         []string // enum / set values
	Subtype         string   // geometry subtype
	SRID            int
	IsNullable      bool
	IsUnsigned      bool
	IsAutoIncrement bool
	StartingValue   int
	HasDefault      bool
	DefaultValue    any
	OnUpdateNow     bool
	CommentText     string
	CharsetName     string
	CollationName   string
	AfterColumn     string
	IsFirst         bool
	IsInvisible     bool
	StoredExpr      string
	VirtualExpr     string
	Identity        string // "BY DEFAULT" or "ALWAYS" (Postgres identity columns)
	Changed         bool   // modify an existing column

	bp       *Blueprint
	refTable string // set by ForeignIDFor
}

// Expr is a raw SQL default value, e.g. schema.Expr("CURRENT_TIMESTAMP").
type Expr string

func (b *Blueprint) add(name, typ string) *Column {
	c := &Column{Name: name, Type: typ, bp: b}
	b.columns = append(b.columns, c)
	return c
}

// ---------------------------------------------------------------------------
// Table options
// ---------------------------------------------------------------------------

func (b *Blueprint) Engine(engine string)   { b.engine = engine }   // MySQL
func (b *Blueprint) Charset(charset string) { b.charset = charset } // MySQL
func (b *Blueprint) Collation(coll string)  { b.collation = coll }  // MySQL
func (b *Blueprint) Comment(comment string) { b.comment = comment } // MySQL / Postgres
func (b *Blueprint) Temporary()             { b.temporary = true }

// ---------------------------------------------------------------------------
// Column types
// ---------------------------------------------------------------------------

// ID is an auto-incrementing big integer primary key named "id".
func (b *Blueprint) ID(name ...string) *Column { return b.BigIncrements(first(name, "id")) }

func (b *Blueprint) increments(name, typ string) *Column {
	c := b.add(name, typ)
	c.IsAutoIncrement, c.IsUnsigned = true, true
	return c
}

func (b *Blueprint) BigIncrements(name string) *Column { return b.increments(name, TypeBigInteger) }
func (b *Blueprint) Increments(name string) *Column    { return b.increments(name, TypeInteger) }
func (b *Blueprint) MediumIncrements(name string) *Column {
	return b.increments(name, TypeMediumInteger)
}

func (b *Blueprint) SmallIncrements(name string) *Column { return b.increments(name, TypeSmallInteger) }

func (b *Blueprint) TinyIncrements(name string) *Column { return b.increments(name, TypeTinyInteger) }

func (b *Blueprint) String(name string, length ...int) *Column {
	c := b.add(name, TypeString)
	c.Length = first(length, 255)
	return c
}

func (b *Blueprint) Char(name string, length ...int) *Column {
	c := b.add(name, TypeChar)
	c.Length = first(length, 255)
	return c
}

func (b *Blueprint) TinyText(name string) *Column   { return b.add(name, TypeTinyText) }
func (b *Blueprint) Text(name string) *Column       { return b.add(name, TypeText) }
func (b *Blueprint) MediumText(name string) *Column { return b.add(name, TypeMediumText) }
func (b *Blueprint) LongText(name string) *Column   { return b.add(name, TypeLongText) }

func (b *Blueprint) TinyInteger(name string) *Column   { return b.add(name, TypeTinyInteger) }
func (b *Blueprint) SmallInteger(name string) *Column  { return b.add(name, TypeSmallInteger) }
func (b *Blueprint) MediumInteger(name string) *Column { return b.add(name, TypeMediumInteger) }
func (b *Blueprint) Integer(name string) *Column       { return b.add(name, TypeInteger) }
func (b *Blueprint) BigInteger(name string) *Column    { return b.add(name, TypeBigInteger) }

func (b *Blueprint) UnsignedTinyInteger(name string) *Column { return b.TinyInteger(name).Unsigned() }

func (b *Blueprint) UnsignedSmallInteger(name string) *Column { return b.SmallInteger(name).Unsigned() }

func (b *Blueprint) UnsignedMediumInteger(name string) *Column {
	return b.MediumInteger(name).Unsigned()
}
func (b *Blueprint) UnsignedInteger(name string) *Column    { return b.Integer(name).Unsigned() }
func (b *Blueprint) UnsignedBigInteger(name string) *Column { return b.BigInteger(name).Unsigned() }

func (b *Blueprint) Boolean(name string) *Column { return b.add(name, TypeBoolean) }

func (b *Blueprint) Decimal(name string, precision, scale int) *Column {
	c := b.add(name, TypeDecimal)
	c.Precision, c.Scale = precision, scale
	return c
}

func (b *Blueprint) Float(name string, precision ...int) *Column {
	c := b.add(name, TypeFloat)
	c.Precision = first(precision, 53)
	return c
}

func (b *Blueprint) Double(name string) *Column { return b.add(name, TypeDouble) }

func (b *Blueprint) Date(name string) *Column        { return b.add(name, TypeDate) }
func (b *Blueprint) Time(name string) *Column        { return b.add(name, TypeTime) }
func (b *Blueprint) TimeTz(name string) *Column      { return b.add(name, TypeTimeTz) }
func (b *Blueprint) DateTime(name string) *Column    { return b.add(name, TypeDateTime) }
func (b *Blueprint) DateTimeTz(name string) *Column  { return b.add(name, TypeDateTimeTz) }
func (b *Blueprint) Timestamp(name string) *Column   { return b.add(name, TypeTimestamp) }
func (b *Blueprint) TimestampTz(name string) *Column { return b.add(name, TypeTimestampTz) }
func (b *Blueprint) Year(name string) *Column        { return b.add(name, TypeYear) }

func (b *Blueprint) JSON(name string) *Column   { return b.add(name, TypeJSON) }
func (b *Blueprint) JSONB(name string) *Column  { return b.add(name, TypeJSONB) }
func (b *Blueprint) Binary(name string) *Column { return b.add(name, TypeBinary) }

func (b *Blueprint) UUID(name ...string) *Column { return b.add(first(name, "uuid"), TypeUUID) }
func (b *Blueprint) ULID(name ...string) *Column { return b.add(first(name, "ulid"), TypeULID) }

// UUIDPrimary is a UUID primary key (HasUuids models).
func (b *Blueprint) UUIDPrimary(name ...string) *Column { return b.UUID(first(name, "id")).Primary() }

// ULIDPrimary is a ULID primary key (HasUlids models).
func (b *Blueprint) ULIDPrimary(name ...string) *Column { return b.ULID(first(name, "id")).Primary() }

func (b *Blueprint) IPAddress(name ...string) *Column {
	return b.add(first(name, "ip_address"), TypeIPAddress)
}

func (b *Blueprint) MACAddress(name ...string) *Column {
	return b.add(first(name, "mac_address"), TypeMACAddress)
}

func (b *Blueprint) Enum(name string, allowed ...string) *Column {
	c := b.add(name, TypeEnum)
	c.Allowed = allowed
	return c
}

// Set is MySQL's SET type (a varchar elsewhere).
func (b *Blueprint) Set(name string, allowed ...string) *Column {
	c := b.add(name, TypeSet)
	c.Allowed = allowed
	return c
}

// Geometry is a spatial column; subtype ("point", "polygon", ...) and SRID are optional.
func (b *Blueprint) Geometry(name string, subtype string, srid int) *Column {
	c := b.add(name, TypeGeometry)
	c.Subtype, c.SRID = subtype, srid
	return c
}

func (b *Blueprint) Geography(name string, subtype string, srid int) *Column {
	c := b.add(name, TypeGeography)
	c.Subtype, c.SRID = subtype, cmpOr(srid, 4326)
	return c
}

// Vector is a pgvector / MySQL 9 vector column.
func (b *Blueprint) Vector(name string, dimensions int) *Column {
	c := b.add(name, TypeVector)
	c.Length = dimensions
	return c
}

// ForeignID is an unsigned big integer meant for a foreign key; chain
// Constrained() to add the constraint.
func (b *Blueprint) ForeignID(name string) *Column   { return b.UnsignedBigInteger(name) }
func (b *Blueprint) ForeignUUID(name string) *Column { return b.UUID(name) }
func (b *Blueprint) ForeignULID(name string) *Column { return b.ULID(name) }

// ForeignIDFor adds a key column referencing a model's table, typed after
// its key: ForeignIDFor(Users.Table) adds user_id (foreignIdFor).
func (b *Blueprint) ForeignIDFor[M any](t *orm.Table[M], column ...string) *Column {
	name := first(column, singular(t.Name)+"_"+cmpOr(t.PrimaryKey, "id"))
	var c *Column
	if t.KeyType == orm.KeyUUID {
		c = b.ForeignUUID(name)
	} else {
		c = b.ForeignID(name)
	}
	c.refTable = t.Name
	return c
}

// Timestamps adds nullable created_at and updated_at.
func (b *Blueprint) Timestamps() {
	b.Timestamp("created_at").Nullable()
	b.Timestamp("updated_at").Nullable()
}

func (b *Blueprint) TimestampsTz() {
	b.TimestampTz("created_at").Nullable()
	b.TimestampTz("updated_at").Nullable()
}

// NullableTimestamps is an alias of Timestamps.
func (b *Blueprint) NullableTimestamps() { b.Timestamps() }

// Datetimes adds nullable created_at and updated_at datetime columns.
func (b *Blueprint) Datetimes() {
	b.DateTime("created_at").Nullable()
	b.DateTime("updated_at").Nullable()
}

// SoftDeletes adds a nullable deleted_at.
func (b *Blueprint) SoftDeletes(name ...string) *Column {
	return b.Timestamp(first(name, "deleted_at")).Nullable()
}

func (b *Blueprint) SoftDeletesTz(name ...string) *Column {
	return b.TimestampTz(first(name, "deleted_at")).Nullable()
}

// Morphs adds {name}_id and {name}_type with a composite index.
func (b *Blueprint) Morphs(name string)     { b.morphs(name, b.UnsignedBigInteger, false) }
func (b *Blueprint) UUIDMorphs(name string) { b.morphs(name, b.uuidCol, false) }
func (b *Blueprint) ULIDMorphs(name string) { b.morphs(name, b.ulidCol, false) }
func (b *Blueprint) NullableMorphs(name string) {
	b.morphs(name, b.UnsignedBigInteger, true)
}
func (b *Blueprint) NullableUUIDMorphs(name string) { b.morphs(name, b.uuidCol, true) }
func (b *Blueprint) NullableULIDMorphs(name string) { b.morphs(name, b.ulidCol, true) }

func (b *Blueprint) uuidCol(name string) *Column { return b.UUID(name) }
func (b *Blueprint) ulidCol(name string) *Column { return b.ULID(name) }

func (b *Blueprint) morphs(name string, id func(string) *Column, nullable bool) {
	idc, typ := id(name+"_id"), b.String(name+"_type")
	if nullable {
		idc.Nullable()
		typ.Nullable()
	}
	b.Index(name+"_type", name+"_id")
}

// RememberToken adds a nullable remember_token string(100).
func (b *Blueprint) RememberToken() *Column { return b.String("remember_token", 100).Nullable() }

// ---------------------------------------------------------------------------
// Column modifiers
// ---------------------------------------------------------------------------

func (c *Column) Nullable(nullable ...bool) *Column {
	c.IsNullable = len(nullable) == 0 || nullable[0]
	return c
}
func (c *Column) Unsigned() *Column              { c.IsUnsigned = true; return c }
func (c *Column) Default(v any) *Column          { c.HasDefault, c.DefaultValue = true, v; return c }
func (c *Column) Comment(text string) *Column    { c.CommentText = text; return c }
func (c *Column) Charset(charset string) *Column { c.CharsetName = charset; return c }
func (c *Column) Collation(coll string) *Column  { c.CollationName = coll; return c }
func (c *Column) After(column string) *Column    { c.AfterColumn = column; return c } // MySQL
func (c *Column) First() *Column                 { c.IsFirst = true; return c }       // MySQL
func (c *Column) Invisible() *Column             { c.IsInvisible = true; return c }   // MySQL

// AutoIncrement makes the column an auto-incrementing primary key.
func (c *Column) AutoIncrement() *Column { c.IsAutoIncrement = true; return c }

// From sets the starting value of an auto-incrementing column.
func (c *Column) From(start int) *Column { c.StartingValue = start; return c }

// StoredAs makes the column a stored generated column.
func (c *Column) StoredAs(expr string) *Column { c.StoredExpr = expr; return c }

// VirtualAs makes the column a virtual generated column.
func (c *Column) VirtualAs(expr string) *Column { c.VirtualExpr = expr; return c }

// GeneratedAs makes the column a Postgres identity column (GENERATED BY DEFAULT AS IDENTITY).
func (c *Column) GeneratedAs() *Column { c.Identity = "BY DEFAULT"; return c }

// Always makes an identity column GENERATED ALWAYS.
func (c *Column) Always() *Column { c.Identity = "ALWAYS"; return c }

// UseCurrent defaults the column to CURRENT_TIMESTAMP.
func (c *Column) UseCurrent() *Column { return c.Default(Expr("CURRENT_TIMESTAMP")) }

// UseCurrentOnUpdate sets the column to CURRENT_TIMESTAMP on update (MySQL).
func (c *Column) UseCurrentOnUpdate() *Column { c.OnUpdateNow = true; return c }

// Change marks the definition as a modification of an existing column
// (SQLite rebuilds the table, like Laravel).
func (c *Column) Change() *Column { c.Changed = true; return c }

func (c *Column) Primary() *Column      { c.bp.Primary(c.Name); return c }
func (c *Column) Unique() *Column       { c.bp.Unique(c.Name); return c }
func (c *Column) Index() *Column        { c.bp.Index(c.Name); return c }
func (c *Column) FullText() *Column     { c.bp.FullText(c.Name); return c }
func (c *Column) SpatialIndex() *Column { c.bp.SpatialIndex(c.Name); return c }
func (c *Column) HnswIndex(metric orm.VectorMetric) *Column {
	c.bp.HnswIndex(c.Name, metric)
	return c
}

// Constrained adds a foreign key on the column. The table defaults to the
// one guessed from the column name (user_id -> users) or the model given to
// ForeignIDFor; the column defaults to "id".
func (c *Column) Constrained(tableAndColumn ...string) *Foreign {
	table := first(tableAndColumn, c.refTable)
	if table == "" {
		table = strings.TrimSuffix(c.Name, "_id") + "s"
	}
	col := "id"
	if len(tableAndColumn) > 1 {
		col = tableAndColumn[1]
	}
	return c.bp.Foreign(c.Name).References(col).On(table)
}

// ---------------------------------------------------------------------------
// Indexes and foreign keys
// ---------------------------------------------------------------------------

// Index is an index definition with chainable modifiers.
type Index struct {
	IndexName string
	Columns   []string
	Algo      string // btree, hash, gin, gist, hnsw, ...
	Lang      string // full text language (Postgres)
	VectorOp  string // pgvector operator class (vector_cosine_ops, ...)
}

func vectorOpClass(m orm.VectorMetric) string {
	switch m {
	case orm.Cosine:
		return "vector_cosine_ops"
	case orm.InnerProduct:
		return "vector_ip_ops"
	default:
		return "vector_l2_ops"
	}
}

func (i *Index) Name(name string) *Index      { i.IndexName = name; return i }
func (i *Index) Algorithm(algo string) *Index { i.Algo = algo; return i }
func (i *Index) Language(lang string) *Index  { i.Lang = lang; return i }

func (b *Blueprint) Primary(cols ...string) *Index      { return b.index("primary", cols) }
func (b *Blueprint) Unique(cols ...string) *Index       { return b.index("unique", cols) }
func (b *Blueprint) Index(cols ...string) *Index        { return b.index("index", cols) }
func (b *Blueprint) FullText(cols ...string) *Index     { return b.index("fullText", cols) }
func (b *Blueprint) SpatialIndex(cols ...string) *Index { return b.index("spatial", cols) }

// HnswIndex builds a pgvector HNSW index over one vector column, making
// Nearest ordering an index-backed search instead of a sequential scan.
func (b *Blueprint) HnswIndex(column string, metric orm.VectorMetric) *Index {
	idx := b.index("hnsw", []string{column})
	idx.Algo = "hnsw"
	idx.VectorOp = vectorOpClass(metric)
	return idx
}

func (b *Blueprint) index(kind string, cols []string) *Index {
	idx := &Index{IndexName: indexName(b.table, kindSuffix(kind), cols), Columns: cols}
	b.commands = append(b.commands, &command{kind: kind, index: idx, columns: cols})
	return idx
}

// RenameIndex renames an index.
func (b *Blueprint) RenameIndex(from, to string) {
	b.commands = append(b.commands, &command{kind: "renameIndex", from: from, to: to})
}

// Foreign starts a foreign key on cols.
func (b *Blueprint) Foreign(cols ...string) *Foreign {
	f := &Foreign{Columns: cols, Name: indexName(b.table, "foreign", cols)}
	b.commands = append(b.commands, &command{kind: "foreign", foreign: f})
	return f
}

// Foreign is a foreign key definition with chainable modifiers.
type Foreign struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnDel      string
	OnUpd      string
	Defer      bool
	InitDefer  bool
}

func (f *Foreign) References(cols ...string) *Foreign { f.RefColumns = cols; return f }
func (f *Foreign) On(table string) *Foreign           { f.RefTable = table; return f }
func (f *Foreign) Named(name string) *Foreign         { f.Name = name; return f }
func (f *Foreign) OnDelete(action string) *Foreign    { f.OnDel = action; return f }
func (f *Foreign) OnUpdate(action string) *Foreign    { f.OnUpd = action; return f }
func (f *Foreign) CascadeOnDelete() *Foreign          { return f.OnDelete("cascade") }
func (f *Foreign) RestrictOnDelete() *Foreign         { return f.OnDelete("restrict") }
func (f *Foreign) NullOnDelete() *Foreign             { return f.OnDelete("set null") }
func (f *Foreign) NoActionOnDelete() *Foreign         { return f.OnDelete("no action") }
func (f *Foreign) CascadeOnUpdate() *Foreign          { return f.OnUpdate("cascade") }
func (f *Foreign) RestrictOnUpdate() *Foreign         { return f.OnUpdate("restrict") }
func (f *Foreign) NullOnUpdate() *Foreign             { return f.OnUpdate("set null") }
func (f *Foreign) NoActionOnUpdate() *Foreign         { return f.OnUpdate("no action") }

// Deferrable makes the constraint deferrable (Postgres).
func (f *Foreign) Deferrable() *Foreign { f.Defer = true; return f }

// InitiallyDeferred makes the constraint deferrable and initially deferred (Postgres).
func (f *Foreign) InitiallyDeferred() *Foreign { f.Defer, f.InitDefer = true, true; return f }

// ---------------------------------------------------------------------------
// Alter commands
// ---------------------------------------------------------------------------

func (b *Blueprint) DropColumn(cols ...string) {
	b.commands = append(b.commands, &command{kind: "dropColumn", columns: cols})
}

func (b *Blueprint) RenameColumn(from, to string) {
	b.commands = append(b.commands, &command{kind: "renameColumn", from: from, to: to})
}

// DropIndex drops an index by name, or by the columns it was created on.
func (b *Blueprint) DropIndex(nameOrCols ...string)    { b.drop("dropIndex", "index", nameOrCols) }
func (b *Blueprint) DropUnique(nameOrCols ...string)   { b.drop("dropIndex", "unique", nameOrCols) }
func (b *Blueprint) DropFullText(nameOrCols ...string) { b.drop("dropIndex", "fulltext", nameOrCols) }

func (b *Blueprint) DropSpatialIndex(nameOrCols ...string) {
	b.drop("dropIndex", "spatialindex", nameOrCols)
}

func (b *Blueprint) DropForeign(nameOrCols ...string) { b.drop("dropForeign", "foreign", nameOrCols) }

func (b *Blueprint) DropPrimary() {
	b.commands = append(b.commands, &command{kind: "dropPrimary", index: &Index{IndexName: indexName(b.table, "primary", nil)}})
}

// DropConstrainedForeignID drops the foreign key and its column.
func (b *Blueprint) DropConstrainedForeignID(column string) {
	b.DropForeign(column)
	b.DropColumn(column)
}

// DropForeignIDFor drops the foreign key column created by ForeignIDFor.
func (b *Blueprint) DropForeignIDFor[M any](t *orm.Table[M], column ...string) {
	b.DropColumn(first(column, singular(t.Name)+"_"+cmpOr(t.PrimaryKey, "id")))
}

func (b *Blueprint) DropTimestamps()    { b.DropColumn("created_at", "updated_at") }
func (b *Blueprint) DropTimestampsTz()  { b.DropTimestamps() }
func (b *Blueprint) DropSoftDeletes()   { b.DropColumn("deleted_at") }
func (b *Blueprint) DropSoftDeletesTz() { b.DropSoftDeletes() }
func (b *Blueprint) DropRememberToken() { b.DropColumn("remember_token") }
func (b *Blueprint) DropMorphs(name string) {
	b.DropIndex(name+"_type", name+"_id")
	b.DropColumn(name+"_type", name+"_id")
}

func (b *Blueprint) drop(kind, idx string, nameOrCols []string) {
	name := nameOrCols[0]
	if len(nameOrCols) > 1 || !strings.HasSuffix(name, "_"+idx) {
		name = indexName(b.table, idx, nameOrCols)
	}
	b.commands = append(b.commands, &command{kind: kind, index: &Index{IndexName: name}})
}

// indexName follows Laravel's convention: users_email_unique.
func indexName(table, suffix string, cols []string) string {
	parts := append(append([]string{table}, cols...), suffix)
	return strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(strings.Join(parts, "_")))
}

func kindSuffix(kind string) string {
	switch kind {
	case "fullText":
		return "fulltext"
	case "spatial":
		return "spatialindex"
	}
	return kind
}

func singular(table string) string {
	switch {
	case strings.HasSuffix(table, "ies"):
		return strings.TrimSuffix(table, "ies") + "y"
	case strings.HasSuffix(table, "ses"), strings.HasSuffix(table, "xes"):
		return table[:len(table)-2]
	}
	return strings.TrimSuffix(table, "s")
}

func first[T comparable](vs []T, def T) T {
	var zero T
	if len(vs) > 0 && vs[0] != zero {
		return vs[0]
	}
	return def
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
