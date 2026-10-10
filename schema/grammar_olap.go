package schema

import "strconv"

// duckdbType maps blueprint types to DuckDB's SQL. DuckDB is
// Postgres-flavoured: same INTEGER/BIGINT/VARCHAR/TIMESTAMP vocabulary, but
// no serial types (auto-increment columns are plain integers app-side or
// via sequences), a JSON type, and FLOAT[N] arrays for vectors.
func duckdbType(c *Column, auto bool) string {
	n := strconv.Itoa
	switch c.Type {
	case TypeString:
		return "varchar(" + n(c.Length) + ")"
	case TypeChar:
		return "char(" + n(c.Length) + ")"
	case TypeTinyText, TypeText, TypeMediumText, TypeLongText:
		return "text"
	case TypeTinyInteger, TypeSmallInteger, TypeMediumInteger, TypeInteger:
		return "integer"
	case TypeBigInteger:
		return "bigint"
	case TypeBoolean:
		return "boolean"
	case TypeDecimal:
		return "decimal(" + n(c.Precision) + ", " + n(c.Scale) + ")"
	case TypeFloat:
		return "float(" + n(c.Precision) + ")"
	case TypeDouble:
		return "double"
	case TypeDate:
		return "date"
	case TypeTime, TypeTimeTz:
		return "time"
	case TypeDateTime, TypeDateTimeTz, TypeTimestamp, TypeTimestampTz:
		return "timestamp"
	case TypeYear:
		return "integer"
	case TypeJSON, TypeJSONB:
		return "json"
	case TypeBinary:
		return "blob"
	case TypeUUID:
		return "uuid"
	case TypeULID, TypeIPAddress, TypeMACAddress, TypeEnum, TypeSet:
		return "varchar(255)"
	case TypeGeometry, TypeGeography:
		return "geometry"
	case TypeVector:
		return "float[" + n(c.Length) + "]"
	}
	panic("schema: unknown column type " + c.Type)
}

// clickhouseType maps blueprint types to ClickHouse. Nullable columns wrap
// the type in Nullable(); there is no serial (explicit or UUID keys only).
func clickhouseType(c *Column) string {
	n := strconv.Itoa
	var t string
	switch c.Type {
	case TypeString, TypeChar:
		t = "String"
		if c.Length > 0 && c.Type == TypeChar {
			t = "FixedString(" + n(c.Length) + ")"
		}
	case TypeTinyText, TypeText, TypeMediumText, TypeLongText:
		t = "String"
	case TypeTinyInteger:
		t = "Int8"
	case TypeSmallInteger:
		t = "Int16"
	case TypeMediumInteger:
		t = "Int32"
	case TypeInteger:
		t = "Int32"
	case TypeBigInteger:
		t = "Int64"
	case TypeBoolean:
		t = "Bool"
	case TypeDecimal:
		t = "Decimal(" + n(c.Precision) + ", " + n(c.Scale) + ")"
	case TypeFloat:
		t = "Float32"
	case TypeDouble:
		t = "Float64"
	case TypeDate:
		t = "Date"
	case TypeTime, TypeTimeTz:
		t = "String"
	case TypeDateTime, TypeDateTimeTz:
		t = "DateTime"
	case TypeTimestamp, TypeTimestampTz:
		t = "DateTime64(3)"
	case TypeYear:
		t = "Int16"
	case TypeJSON, TypeJSONB:
		t = "String"
	case TypeBinary:
		t = "String"
	case TypeUUID, TypeULID:
		t = "String"
	case TypeIPAddress:
		t = "IPv4"
	case TypeMACAddress:
		t = "String"
	case TypeEnum, TypeSet:
		t = "String"
	case TypeGeometry, TypeGeography:
		t = "String"
	case TypeVector:
		t = "Array(Float64)"
	}
	if c.IsNullable && c.Type != TypeVector {
		return "Nullable(" + t + ")"
	}
	return t
}
