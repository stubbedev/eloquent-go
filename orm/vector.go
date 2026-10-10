package orm

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// VectorMetric is the distance metric for nearest-neighbour ordering.
type VectorMetric int

const (
	// L2 is squared Euclidean distance (pgvector <->, array_distance).
	L2 VectorMetric = iota
	// Cosine is 1 - cosine similarity (pgvector <=>).
	Cosine
	// InnerProduct is the negative dot product ordering of pgvector (<#>);
	// plain similarity on other engines.
	InnerProduct
)

// Vector is a fixed-size float vector column — pgvector's vector type,
// DuckDB's FLOAT[N], Qdrant's named vectors. Each dimension is its own Go
// type via the D- markers (Go cannot take a number as a type argument), so
// Docs.Embedding (Vector[D3]) and Docs.Fingerprint (Vector[D128]) cannot be
// mixed, and a Vector column cannot be compared to a string.
//
//	type Doc struct {
//		orm.Model
//		ID        int64            `db:"id"`
//		Embedding orm.Vector[orm.D3] `db:"embedding"`
//	}
//
// Ordering with Nearest runs a nearest-neighbour search on the engine —
// an index-backed one with pgvector's HNSW or a Qdrant collection.
type Vector[N ~int] struct{ comps []float64 }

// Dimension markers for Vector's type argument: orm.Vector[orm.D3] is a
// 3-dimensional vector. The dimension is enforced by the schema
// (vector(3), a Qdrant collection's size), since Go cannot recover N from
// the type parameter.
type (
	D2    int
	D3    int
	D4    int
	D5    int
	D6    int
	D7    int
	D8    int
	D9    int
	D10   int
	D12   int
	D16   int
	D24   int
	D32   int
	D48   int
	D64   int
	D96   int
	D128  int
	D160  int
	D256  int
	D384  int
	D512  int
	D640  int
	D768  int
	D896  int
	D1024 int
	D1280 int
	D1536 int
	D2048 int
	D3072 int
	D4096 int
)

// NewVector builds a Vector of dimension N from its components. The
// dimension is enforced by the schema (vector(3), a Qdrant collection's
// size), not at construction: Go cannot recover N from a type parameter.
func NewVector[N ~int](fs ...float64) Vector[N] {
	return Vector[N]{comps: append([]float64(nil), fs...)}
}

// NewVector3 is shorthand for NewVector[orm.D3].
func NewVector3(x, y, z float64) Vector[D3] { return NewVector[D3](x, y, z) }

// Floats returns the components; nil for the zero Vector.
func (v Vector[N]) Floats() []float64 { return v.comps }

func (v Vector[N]) String() string {
	fs := make([]string, len(v.comps))
	for i, f := range v.comps {
		fs[i] = strconv.FormatFloat(f, 'g', -1, 64)
	}
	return "[" + strings.Join(fs, ",") + "]"
}

// Value stores the vector in the text form pgvector accepts; DuckDB casts
// the same form to FLOAT[N].
func (v Vector[N]) Value() (driver.Value, error) { return v.String(), nil }

// Scan reads the text form back (pgvector returns it in queries).
func (v *Vector[N]) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = Vector[N]{}
		return nil
	case string:
		return v.parse(s)
	case []byte:
		return v.parse(string(s))
	case []float64:
		v.comps = append([]float64(nil), s...)
		return nil
	case []float32:
		comps := make([]float64, len(s))
		for i, f := range s {
			comps[i] = float64(f)
		}
		v.comps = comps
		return nil
	case []any:
		comps := make([]float64, len(s))
		for i, f := range s {
			if n, err := toFloat(f); err != nil {
				return err
			} else {
				comps[i] = n
			}
		}
		v.comps = comps
		return nil
	}
	return fmt.Errorf("orm: cannot scan %T into a Vector", src)
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		return strconv.ParseFloat(n, 64)
	}
	return 0, fmt.Errorf("not a number: %T", v)
}

func (v *Vector[N]) parse(s string) error {
	if strings.TrimSpace(s) == "" {
		*v = Vector[N]{}
		return nil
	}
	fs := strings.Split(strings.Trim(s, "[] "), ",")
	if len(v.comps) != 0 && len(fs) != len(v.comps) {
		return fmt.Errorf("orm: vector of %d components cannot scan %q", len(v.comps), s)
	}
	comps := make([]float64, len(fs))
	for i, f := range fs {
		x, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil {
			return fmt.Errorf("orm: scanning vector %q: %w", s, err)
		}
		comps[i] = x
	}
	v.comps = comps
	return nil
}

func (v Vector[N]) MarshalJSON() ([]byte, error) {
	if v.comps == nil {
		return []byte("null"), nil
	}
	return json.Marshal(v.comps)
}

func (v *Vector[N]) UnmarshalJSON(b []byte) error {
	var fs []float64
	if err := json.Unmarshal(b, &fs); err != nil {
		return err
	}
	if len(v.comps) != 0 && len(fs) != len(v.comps) {
		return fmt.Errorf("orm: vector of %d components cannot unmarshal %d values", len(v.comps), len(fs))
	}
	v.comps = fs
	return nil
}

// Nearest orders rows by distance from v under the metric (pgvector's
// nearest-neighbours, DuckDB's array_distance, a Qdrant search). Combined
// with Limit it is a KNN query.
func (s Scalar[M, V]) Nearest(v V, metric VectorMetric) Order[M] {
	o := Order[M]{f: func(b *SQL) { b.Dialect.VectorDistance(b, s, v, metric) }}
	if s.col != "" {
		if vv, ok := any(v).(interface {
			Floats() []float64
			Value() (driver.Value, error)
		}); ok {
			o.vec = &VectorOrder{Column: s.col, Floats: vv.Floats(), Metric: metric}
		}
	}
	return o
}
