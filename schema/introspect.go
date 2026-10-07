package schema

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// ColumnInfo describes an existing column (Schema::getColumns).
type ColumnInfo struct {
	Name          string
	Type          string
	Nullable      bool
	Default       *string
	AutoIncrement bool
}

// IndexInfo describes an existing index (Schema::getIndexes).
type IndexInfo struct {
	Name    string
	Columns []string
	Unique  bool
	Primary bool
}

// ForeignKeyInfo describes an existing foreign key (Schema::getForeignKeys).
type ForeignKeyInfo struct {
	Name           string
	Columns        []string
	ForeignTable   string
	ForeignColumns []string
	OnUpdate       string
	OnDelete       string
}

// Tables lists the user tables on the connection (Schema::getTableListing).
func (s *Builder) Tables(ctx context.Context) ([]string, error) {
	var out []string
	err := s.query(ctx, map[string]string{
		"sqlite":   "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
		"postgres": "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name",
		"mysql":    "SELECT table_name FROM information_schema.tables WHERE table_schema = database() AND table_type = 'BASE TABLE' ORDER BY table_name",
	}, nil, func(r *sql.Rows) error {
		var n string
		out = append(out, n)
		return r.Scan(&out[len(out)-1])
	})
	return out, err
}

// Columns lists column names (Schema::getColumnListing).
func (s *Builder) Columns(ctx context.Context, table string) ([]string, error) {
	cols, err := s.GetColumns(ctx, table)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names, err
}

// GetColumns describes the columns of table.
func (s *Builder) GetColumns(ctx context.Context, table string) ([]ColumnInfo, error) {
	var out []ColumnInfo
	autoinc := false
	if g, err := s.kind(ctx); err == nil && g == "sqlite" {
		var create sql.NullString
		s.query(ctx, map[string]string{"sqlite": "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?"},
			[]any{table}, func(r *sql.Rows) error { return r.Scan(&create) })
		autoinc = strings.Contains(strings.ToLower(create.String), "autoincrement")
	}
	err := s.query(ctx, map[string]string{
		"sqlite":   `SELECT name, type, "notnull" = 0, dflt_value, pk = 1 AND lower(type) = 'integer' FROM pragma_table_xinfo(?) WHERE hidden != 1 ORDER BY cid`,
		"postgres": `SELECT column_name, data_type, is_nullable = 'YES', column_default, coalesce(column_default LIKE 'nextval(%', false) OR is_identity = 'YES' FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`,
		"mysql":    `SELECT column_name, column_type, is_nullable = 'YES', column_default, extra LIKE '%auto_increment%' FROM information_schema.columns WHERE table_schema = database() AND table_name = ? ORDER BY ordinal_position`,
	}, []any{table}, func(r *sql.Rows) error {
		var c ColumnInfo
		var def sql.NullString
		var auto bool
		if err := r.Scan(&c.Name, &c.Type, &c.Nullable, &def, &auto); err != nil {
			return err
		}
		if def.Valid {
			c.Default = &def.String
		}
		c.AutoIncrement = auto && (autoinc || s.kindOrEmpty(ctx) != "sqlite")
		out = append(out, c)
		return nil
	})
	return out, err
}

// GetIndexes describes the indexes of table, including the primary key.
func (s *Builder) GetIndexes(ctx context.Context, table string) ([]IndexInfo, error) {
	var out []IndexInfo
	err := s.query(ctx, map[string]string{
		"sqlite": `SELECT il.name, group_concat(ii.name, ','), il."unique", il.origin = 'pk'
			FROM pragma_index_list(?) AS il JOIN pragma_index_info(il.name) AS ii
			GROUP BY il.name, il."unique", il.origin`,
		"postgres": `SELECT ic.relname, coalesce(string_agg(a.attname, ',' ORDER BY array_position(i.indkey::int2[], a.attnum)), ''), i.indisunique, i.indisprimary
			FROM pg_index i
			JOIN pg_class tc ON tc.oid = i.indrelid
			JOIN pg_class ic ON ic.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = tc.relnamespace
			LEFT JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum = ANY(i.indkey) AND a.attnum > 0
			WHERE tc.relname = $1 AND n.nspname = current_schema()
			GROUP BY ic.relname, i.indisunique, i.indisprimary`,
		"mysql": `SELECT index_name, group_concat(column_name ORDER BY seq_in_index), non_unique = 0, index_name = 'PRIMARY'
			FROM information_schema.statistics WHERE table_schema = database() AND table_name = ?
			GROUP BY index_name, non_unique`,
	}, []any{table}, func(r *sql.Rows) error {
		var i IndexInfo
		var cols string
		if err := r.Scan(&i.Name, &cols, &i.Unique, &i.Primary); err != nil {
			return err
		}
		if cols != "" {
			i.Columns = strings.Split(cols, ",")
		}
		out = append(out, i)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// SQLite rowid primary keys have no index entry; report them like Laravel.
	if k, _ := s.kind(ctx); k == "sqlite" && !slices.ContainsFunc(out, func(i IndexInfo) bool { return i.Primary }) {
		var pk []string
		s.query(ctx, map[string]string{"sqlite": "SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk"},
			[]any{table}, func(r *sql.Rows) error {
				var n string
				err := r.Scan(&n)
				pk = append(pk, n)
				return err
			})
		if len(pk) > 0 {
			out = append(out, IndexInfo{Name: "primary", Columns: pk, Unique: true, Primary: true})
		}
	}
	slices.SortFunc(out, func(a, b IndexInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// GetForeignKeys describes the foreign keys of table. SQLite does not name
// foreign keys; they are reported under Laravel's conventional name.
func (s *Builder) GetForeignKeys(ctx context.Context, table string) ([]ForeignKeyInfo, error) {
	var out []ForeignKeyInfo
	actions := map[string]string{"a": "no action", "r": "restrict", "c": "cascade", "n": "set null", "d": "set default"}
	err := s.query(ctx, map[string]string{
		"sqlite": `SELECT id, group_concat("from", ','), "table", group_concat("to", ','), on_update, on_delete
			FROM (SELECT * FROM pragma_foreign_key_list(?) ORDER BY id, seq) GROUP BY id, "table", on_update, on_delete`,
		"postgres": `SELECT c.conname, string_agg(la.attname, ',' ORDER BY k.ord), ft.relname, string_agg(fa.attname, ',' ORDER BY k.ord), c.confupdtype, c.confdeltype
			FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_class ft ON ft.oid = c.confrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			CROSS JOIN LATERAL unnest(c.conkey, c.confkey) WITH ORDINALITY AS k(l, f, ord)
			JOIN pg_attribute la ON la.attrelid = c.conrelid AND la.attnum = k.l
			JOIN pg_attribute fa ON fa.attrelid = c.confrelid AND fa.attnum = k.f
			WHERE c.contype = 'f' AND t.relname = $1 AND n.nspname = current_schema()
			GROUP BY c.conname, ft.relname, c.confupdtype, c.confdeltype`,
		"mysql": `SELECT kc.constraint_name, group_concat(kc.column_name ORDER BY kc.ordinal_position), kc.referenced_table_name,
				group_concat(kc.referenced_column_name ORDER BY kc.ordinal_position), rc.update_rule, rc.delete_rule
			FROM information_schema.key_column_usage kc
			JOIN information_schema.referential_constraints rc
				ON rc.constraint_schema = kc.table_schema AND rc.constraint_name = kc.constraint_name
			WHERE kc.table_schema = database() AND kc.table_name = ? AND kc.referenced_table_name IS NOT NULL
			GROUP BY kc.constraint_name, kc.referenced_table_name, rc.update_rule, rc.delete_rule`,
	}, []any{table}, func(r *sql.Rows) error {
		var f ForeignKeyInfo
		var cols, fcols string
		if err := r.Scan(&f.Name, &cols, &f.ForeignTable, &fcols, &f.OnUpdate, &f.OnDelete); err != nil {
			return err
		}
		f.Columns, f.ForeignColumns = strings.Split(cols, ","), strings.Split(fcols, ",")
		if a, ok := actions[f.OnUpdate]; ok {
			f.OnUpdate, f.OnDelete = a, actions[f.OnDelete]
		}
		f.OnUpdate, f.OnDelete = strings.ToLower(f.OnUpdate), strings.ToLower(f.OnDelete)
		if k, _ := s.kind(ctx); k == "sqlite" {
			f.Name = indexName(table, "foreign", f.Columns)
		}
		out = append(out, f)
		return nil
	})
	slices.SortFunc(out, func(a, b ForeignKeyInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

// HasIndex reports whether table has an index with the given name or on
// exactly the given columns; kind optionally filters by "unique" or "primary".
func (s *Builder) HasIndex(ctx context.Context, table string, nameOrCols []string, kind ...string) (bool, error) {
	idxs, err := s.GetIndexes(ctx, table)
	if err != nil {
		return false, err
	}
	for _, i := range idxs {
		match := len(nameOrCols) == 1 && i.Name == nameOrCols[0] || slices.Equal(i.Columns, nameOrCols)
		switch first(kind, "") {
		case "unique":
			match = match && i.Unique
		case "primary":
			match = match && i.Primary
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

// GetColumnType returns the database type of a column.
func (s *Builder) GetColumnType(ctx context.Context, table, column string) (string, error) {
	cols, err := s.GetColumns(ctx, table)
	for _, c := range cols {
		if c.Name == column {
			return c.Type, err
		}
	}
	return "", cmpOr(err, fmt.Errorf("schema: column %s.%s not found", table, column))
}

func (s *Builder) kind(ctx context.Context) (string, error) {
	_, g, err := s.grammar(ctx)
	return g.kind, err
}

func (s *Builder) kindOrEmpty(ctx context.Context) string {
	k, _ := s.kind(ctx)
	return k
}

// query runs the dialect's query and calls each for every row.
func (s *Builder) query(ctx context.Context, queries map[string]string, args []any, each func(*sql.Rows) error) error {
	c, g, err := s.grammar(ctx)
	if err != nil {
		return err
	}
	q, ok := queries[g.kind]
	if !ok {
		return fmt.Errorf("schema: introspection not supported on %s", g.kind)
	}
	rows, err := c.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
