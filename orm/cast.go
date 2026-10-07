package orm

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON stores V as JSON text — Eloquent's 'array' / 'object' / AsCollection
// casts. Other casts are plain Go types (time.Time, bool, typed string
// enums) or any type implementing sql.Scanner and driver.Valuer.
type JSON[V any] struct{ V V }

func (j JSON[V]) Value() (driver.Value, error) {
	b, err := json.Marshal(j.V)
	return string(b), err
}

func (j *JSON[V]) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		var zero V
		j.V = zero
		return nil
	case string:
		return json.Unmarshal([]byte(s), &j.V)
	case []byte:
		return json.Unmarshal(s, &j.V)
	}
	return fmt.Errorf("orm: cannot scan %T into JSON", src)
}

func (j JSON[V]) MarshalJSON() ([]byte, error)  { return json.Marshal(j.V) }
func (j *JSON[V]) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &j.V) }
