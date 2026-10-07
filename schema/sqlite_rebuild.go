package schema

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// rebuildSQLite applies alterations SQLite's ALTER TABLE cannot express —
// changing columns, adding or dropping primary and foreign keys, renaming
// indexes — by recreating the table, as Laravel does:
//
//	CREATE TABLE __temp__t (...new definition...)
//	INSERT INTO __temp__t (...) SELECT ... FROM t
//	DROP TABLE t
//	ALTER TABLE __temp__t RENAME TO t
//	recreate indexes
//
// Foreign key enforcement should be off while this runs; PRAGMA
// foreign_keys has no effect inside a transaction, so run such migrations
// with NoTransaction if enforcement is on.
func (s *Builder) rebuildSQLite(ctx context.Context, g grammar, bp *Blueprint) error {
	table := bp.table
	infos, err := s.GetColumns(ctx, table)
	if err != nil {
		return err
	}
	fkInfos, err := s.GetForeignKeys(ctx, table)
	if err != nil {
		return err
	}
	idxInfos, err := s.GetIndexes(ctx, table)
	if err != nil {
		return err
	}

	type col struct {
		name, old, def string
		generated      bool
	}
	var createSQL string
	s.query(ctx, map[string]string{"sqlite": "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?"},
		[]any{table}, func(r *sql.Rows) error { return r.Scan(&createSQL) })
	original := columnDefs(createSQL)
	type xcol struct {
		name      string
		generated bool
	}
	var order []xcol
	s.query(ctx, map[string]string{"sqlite": "SELECT name, hidden IN (2, 3) FROM pragma_table_xinfo(?) ORDER BY cid"},
		[]any{table}, func(r *sql.Rows) error {
			var c xcol
			err := r.Scan(&c.name, &c.generated)
			order = append(order, c)
			return err
		})
	type index struct {
		name, sql string
		cols      []string
	}
	var cols []col
	var pk []string
	autoinc := false
	for _, ix := range idxInfos {
		if ix.Primary {
			pk = ix.Columns
		}
	}
	for _, c := range infos {
		if c.AutoIncrement {
			autoinc = true
		}
	}
	for _, c := range order {
		cols = append(cols, col{c.name, c.name, original[c.name], c.generated})
	}
	var indexes []index
	err = s.query(ctx, map[string]string{"sqlite": "SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL"},
		[]any{table}, func(r *sql.Rows) error {
			var ix index
			if err := r.Scan(&ix.name, &ix.sql); err != nil {
				return err
			}
			for _, info := range idxInfos {
				if info.Name == ix.name {
					ix.cols = info.Columns
				}
			}
			indexes = append(indexes, ix)
			return nil
		})
	if err != nil {
		return err
	}
	fks := make([]*Foreign, len(fkInfos))
	for i, f := range fkInfos {
		fks[i] = &Foreign{Name: f.Name, Columns: f.Columns, RefTable: f.ForeignTable, RefColumns: f.ForeignColumns}
		if f.OnDelete != "no action" {
			fks[i].OnDel = f.OnDelete
		}
		if f.OnUpdate != "no action" {
			fks[i].OnUpd = f.OnUpdate
		}
	}

	find := func(name string) int { return slices.IndexFunc(cols, func(c col) bool { return c.name == name }) }
	var newIndexes []string

	for _, c := range bp.columns {
		def := g.column(c)
		if i := find(c.Name); c.Changed && i >= 0 {
			cols[i].def = def
		} else {
			cols = append(cols, col{c.Name, "", def, false})
		}
		if c.IsAutoIncrement {
			autoinc, pk = true, []string{c.Name}
		}
	}
	for _, cmd := range bp.commands {
		switch cmd.kind {
		case "dropColumn":
			for _, name := range cmd.columns {
				if i := find(name); i >= 0 {
					cols = slices.Delete(cols, i, i+1)
				}
				indexes = slices.DeleteFunc(indexes, func(ix index) bool { return slices.Contains(ix.cols, name) })
				fks = slices.DeleteFunc(fks, func(f *Foreign) bool { return slices.Contains(f.Columns, name) })
				pk = slices.DeleteFunc(pk, func(c string) bool { return c == name })
			}
		case "renameColumn":
			if i := find(cmd.from); i >= 0 {
				cols[i].name = cmd.to
				_, rest := splitName(cols[i].def)
				cols[i].def = g.quote(cmd.to) + rest
			}
			rename := func(list []string) {
				for i, c := range list {
					if c == cmd.from {
						list[i] = cmd.to
					}
				}
			}
			rename(pk)
			for _, f := range fks {
				rename(f.Columns)
			}
			for i := range indexes {
				if slices.Contains(indexes[i].cols, cmd.from) {
					indexes[i].sql = strings.ReplaceAll(indexes[i].sql, g.quote(cmd.from), g.quote(cmd.to))
					rename(indexes[i].cols)
				}
			}
		case "primary":
			pk, autoinc = cmd.columns, false
		case "dropPrimary":
			pk, autoinc = nil, false
			for i := range cols {
				cols[i].def = strings.Replace(cols[i].def, " PRIMARY KEY AUTOINCREMENT", "", 1)
			}
		case "foreign":
			fks = append(fks, cmd.foreign)
		case "dropForeign":
			fks = slices.DeleteFunc(fks, func(f *Foreign) bool { return f.Name == cmd.index.IndexName })
		case "unique", "index", "fullText", "spatial":
			stmt, err := g.createIndex(table, cmd)
			if err != nil {
				return err
			}
			newIndexes = append(newIndexes, stmt)
		case "dropIndex":
			indexes = slices.DeleteFunc(indexes, func(ix index) bool { return ix.name == cmd.index.IndexName })
		case "renameIndex":
			for i := range indexes {
				if indexes[i].name == cmd.from {
					indexes[i].sql = strings.Replace(indexes[i].sql, g.quote(cmd.from), g.quote(cmd.to), 1)
					if !strings.Contains(indexes[i].sql, g.quote(cmd.to)) { // index created without quotes
						indexes[i].sql = strings.Replace(indexes[i].sql, cmd.from, g.quote(cmd.to), 1)
					}
					indexes[i].name = cmd.to
				}
			}
		}
	}

	tmp := "__temp__" + table
	var defs []string
	for _, c := range cols {
		defs = append(defs, c.def)
	}
	if len(pk) > 0 && !autoinc {
		defs = append(defs, "PRIMARY KEY ("+g.list(pk)+")")
	}
	for _, f := range fks {
		defs = append(defs, g.foreign(f))
	}
	var from, to []string
	for _, c := range cols {
		if c.old != "" && !c.generated {
			from, to = append(from, g.quote(c.old)), append(to, g.quote(c.name))
		}
	}
	var fkOn bool
	s.query(ctx, map[string]string{"sqlite": "PRAGMA foreign_keys"}, nil, func(r *sql.Rows) error { return r.Scan(&fkOn) })
	var stmts []string
	if fkOn {
		stmts = append(stmts, g.foreignKeyChecks(false))
	}
	stmts = append(stmts,
		"CREATE TABLE "+g.quote(tmp)+" ("+strings.Join(defs, ", ")+")",
		"INSERT INTO "+g.quote(tmp)+" ("+strings.Join(to, ", ")+") SELECT "+strings.Join(from, ", ")+" FROM "+g.quote(table),
		"DROP TABLE "+g.quote(table),
		"ALTER TABLE "+g.quote(tmp)+" RENAME TO "+g.quote(table),
	)
	for _, ix := range indexes {
		stmts = append(stmts, ix.sql)
	}
	stmts = append(stmts, newIndexes...)
	if fkOn {
		stmts = append(stmts, g.foreignKeyChecks(true))
	}
	return s.exec(ctx, stmts)
}

// columnDefs splits a CREATE TABLE statement into its column definitions,
// keyed by column name, preserving CHECK, GENERATED, COLLATE and other
// clauses that SQLite's pragmas don't report. Table constraints are skipped;
// primary and foreign keys are recomputed from pragmas.
func columnDefs(create string) map[string]string {
	out := map[string]string{}
	open, end := strings.IndexByte(create, '('), strings.LastIndexByte(create, ')')
	if open < 0 || end <= open {
		return out
	}
	for _, part := range splitTopLevel(create[open+1 : end]) {
		part = strings.TrimSpace(part)
		name, _ := splitName(part)
		switch strings.ToUpper(name) {
		case "CONSTRAINT", "PRIMARY", "FOREIGN", "UNIQUE", "CHECK":
			continue
		}
		out[name] = part
	}
	return out
}

// splitTopLevel splits on commas outside parentheses and quotes.
func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '[':
			quote = ']'
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// splitName returns the (unquoted) leading identifier of a definition and the rest.
func splitName(def string) (name, rest string) {
	def = strings.TrimSpace(def)
	if def == "" {
		return "", ""
	}
	closer := map[byte]byte{'"': '"', '`': '`', '[': ']'}[def[0]]
	if closer == 0 {
		i := strings.IndexAny(def, " \t\n")
		if i < 0 {
			return def, ""
		}
		return def[:i], def[i:]
	}
	for i := 1; i < len(def); i++ {
		if def[i] == closer {
			if closer == '"' && i+1 < len(def) && def[i+1] == '"' {
				i++
				continue
			}
			return strings.ReplaceAll(def[1:i], `""`, `"`), def[i+1:]
		}
	}
	return def, ""
}
