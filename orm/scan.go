package orm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// nullable wraps a scan destination so that NULL becomes the field's zero
// value (nil for pointer fields) instead of a scan error, and converts the
// value representations drivers return (e.g. []byte numbers from MySQL,
// int64 booleans from SQLite).
type nullable struct{ dst any }

func (n nullable) Scan(src any) error {
	if s, ok := n.dst.(sql.Scanner); ok {
		return s.Scan(src)
	}
	return assign(reflect.ValueOf(n.dst).Elem(), src)
}

func assign(dst reflect.Value, src any) error {
	if src == nil {
		dst.SetZero()
		return nil
	}
	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		if s, ok := dst.Interface().(sql.Scanner); ok {
			return s.Scan(src)
		}
		return assign(dst.Elem(), src)
	}
	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(dst.Type()) {
		dst.Set(sv)
		return nil
	}
	if b, ok := src.([]byte); ok {
		src, sv = string(b), reflect.ValueOf(string(b))
	}
	switch dst.Kind() {
	case reflect.String:
		if s, ok := src.(string); ok {
			dst.SetString(s)
			return nil
		}
		dst.SetString(fmt.Sprint(src))
		return nil
	case reflect.Slice:
		if s, ok := src.(string); ok && dst.Type().Elem().Kind() == reflect.Uint8 {
			dst.SetBytes([]byte(s))
			return nil
		}
	case reflect.Bool:
		switch v := src.(type) {
		case bool:
			dst.SetBool(v)
			return nil
		case int64:
			dst.SetBool(v != 0)
			return nil
		case string:
			b, err := strconv.ParseBool(v)
			dst.SetBool(b)
			return err
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if s, ok := src.(string); ok {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("orm: cannot convert %q to %s", s, dst.Type())
			}
			sv = reflect.ValueOf(f)
		}
		if b, ok := src.(bool); ok {
			sv = reflect.ValueOf(map[bool]int64{true: 1, false: 0}[b])
		}
		if sv.CanConvert(dst.Type()) {
			dst.Set(sv.Convert(dst.Type()))
			return nil
		}
	case reflect.Struct:
		if dst.Type() == reflect.TypeOf(time.Time{}) {
			if s, ok := src.(string); ok {
				for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05.999999999-07:00", "2006-01-02 15:04:05", "2006-01-02T15:04:05Z", "2006-01-02"} {
					if t, err := time.Parse(layout, s); err == nil {
						dst.Set(reflect.ValueOf(t))
						return nil
					}
				}
			}
		}
	}
	if sv.CanConvert(dst.Type()) {
		dst.Set(sv.Convert(dst.Type()))
		return nil
	}
	// Document stores hand back JSON-shaped values (maps, slices); round-trip
	// them so custom casts (orm.JSON, orm.Vector) hydrate too.
	if b, err := json.Marshal(src); err == nil {
		if err := json.Unmarshal(b, dst.Addr().Interface()); err == nil {
			return nil
		}
	}
	return fmt.Errorf("orm: cannot scan %T into %s", src, dst.Type())
}
