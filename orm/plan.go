package orm

import (
	"errors"
)

// Plan is the engine-independent form of a query: what document stores
// (MongoDB, Qdrant) translate instead of SQL. It is built from the typed
// builder, so the same Users.Where(Users.Karma.Gt(10)).Limit(5) runs on
// Postgres or MongoDB unchanged. SQL-only features (joins, unions, raw
// fragments) make a plan untranslatable, and the store reports which part.
type Plan struct {
	Table   string
	Columns []string     // empty: all columns
	Where   Node         // nil: no filter
	Orders  []PlanOrder  // column orders
	Vector  *VectorOrder // nearest-neighbour order, when any
	Limit   int
	Offset  int
}

// PlanOrder is one ORDER BY term.
type PlanOrder struct {
	Column string
	Desc   bool
}

// VectorOrder is a nearest-neighbour search: rows ranked by distance from
// Floats under Metric (Qdrant search, pgvector KNN).
type VectorOrder struct {
	Column string
	Floats []float64
	Metric VectorMetric
}

// Node is a condition in a Plan.
type Node interface{ node() }

// Field compares one column to one value: =, <>, >, >=, <, <=, LIKE.
type Field struct {
	Table  string // owning table, for detecting joined columns
	Column string
	Op     string
	Value  any
}

// List is IN / NOT IN over values.
type List struct {
	Table  string
	Column string
	Not    bool
	Values []any
}

// Range is BETWEEN.
type Range struct {
	Table  string
	Column string
	Lo, Hi any
	Not    bool
}

// NullTest is IS NULL / IS NOT NULL.
type NullTest struct {
	Table  string
	Column string
	Not    bool
}

// Composite is AND / OR over parts.
type Composite struct {
	Or    bool
	Parts []Node
}

// Negate is NOT part.
type Negate struct{ Part Node }

func (Field) node()     {}
func (List) node()      {}
func (Range) node()     {}
func (NullTest) node()  {}
func (Composite) node() {}
func (Negate) node()    {}

// notTranslatable marks SQL-only conditions (raw SQL, subqueries, joins on
// other tables, JSON functions) so document stores can name the offender.
type notTranslatable struct{ reason string }

func (notTranslatable) node() {}

// ErrNotTranslatable reports a query part that has no equivalent on a
// document store: joins, subqueries, unions, raw SQL fragments and the like.
type ErrNotTranslatable struct{ Reason string }

func (e *ErrNotTranslatable) Error() string {
	return "orm: not supported on a document store: " + e.Reason
}

// IsNotTranslatable reports whether err is a document-store translation
// refusal.
func IsNotTranslatable(err error) bool {
	var e *ErrNotTranslatable
	return errors.As(err, &e)
}
