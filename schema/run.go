package schema

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// Run implements an artisan-style migration CLI. A project's cmd/migrate is:
//
//	func main() {
//		orm.Open(orm.DefaultConnection, "sqlite", "file:app.db")
//		err := schema.Run(context.Background(), schema.NewMigrator(), os.Args[1:], os.Stdout)
//		...
//	}
//
// Commands:
//
//	migrate [-step] [-pretend]   run pending migrations
//	rollback [-step N]           revert the last batch (or N migrations)
//	reset                        revert all migrations
//	refresh                      reset, then migrate
//	fresh                        drop all tables, then migrate
//	wipe                         drop all tables (db:wipe)
//	status                       list migrations and whether they ran
//	make <name> [-dir migrations] [-package migrations]
//	                             scaffold a migration file
func Run(ctx context.Context, m *Migrator, args []string, out io.Writer) error {
	if len(args) == 0 {
		args = []string{"migrate"}
	}
	cmd, rest := strings.TrimPrefix(args[0], "migrate:"), args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	step := fs.Bool("step", false, "run each migration in its own batch")
	pretend := fs.Bool("pretend", false, "print the SQL instead of running it")
	steps := fs.Int("steps", 0, "number of migrations to roll back")
	dir := fs.String("dir", "migrations", "directory for new migrations")
	pkg := fs.String("package", "migrations", "package name for new migrations")
	database := fs.String("database", "", "connection holding the migrations table")
	// Allow flags after positional args (make <name> -dir x).
	var positional []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if rest = fs.Args(); len(rest) > 0 {
			positional, rest = append(positional, rest[0]), rest[1:]
		}
	}

	if *database != "" {
		c := *m
		c.Connection = *database
		m = &c
	}
	report := func(verb string, names []string, err error) error {
		for _, n := range names {
			fmt.Fprintf(out, "%-12s %s\n", verb, n)
		}
		if len(names) == 0 && err == nil {
			fmt.Fprintln(out, "Nothing to do.")
		}
		return err
	}

	switch cmd {
	case "migrate":
		if *pretend {
			sqls, err := m.Pretend(ctx)
			for name, stmts := range sqls {
				fmt.Fprintf(out, "-- %s\n%s;\n", name, strings.Join(stmts, ";\n"))
			}
			return err
		}
		names, err := m.Migrate(ctx, *step)
		return report("Migrated", names, err)
	case "rollback":
		names, err := m.Rollback(ctx, *steps)
		return report("Rolled back", names, err)
	case "reset":
		names, err := m.Reset(ctx)
		return report("Rolled back", names, err)
	case "refresh":
		names, err := m.Refresh(ctx)
		return report("Migrated", names, err)
	case "fresh":
		names, err := m.Fresh(ctx)
		return report("Migrated", names, err)
	case "wipe":
		if err := On(m.conn()).DropAllTables(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "Dropped all tables.")
		return nil
	case "status":
		st, err := m.Status(ctx)
		for _, s := range st {
			state := "Pending"
			if s.Ran {
				state = fmt.Sprintf("Ran [%d]", s.Batch)
			}
			fmt.Fprintf(out, "%-10s %s\n", state, s.Name)
		}
		return err
	case "make":
		if len(positional) != 1 {
			return fmt.Errorf("usage: make <name>")
		}
		path, err := MakeMigration(*dir, *pkg, positional[0], time.Now())
		if err == nil {
			fmt.Fprintln(out, "Created", path)
		}
		return err
	}
	return fmt.Errorf("unknown command %q", cmd)
}

var (
	createRe = regexp.MustCompile(`^create_(\w+?)_table$`)
	alterRe  = regexp.MustCompile(`^\w+_(?:to|from|in)_(\w+?)(?:_table)?$`)
	makeTmpl = template.Must(template.New("").Parse(`package {{.Package}}

import (
	"context"

	"github.com/stubbedev/eloquent-go/schema"
)

func init() {
	schema.Register(schema.Migration{
		Name: "{{.Name}}",
		Up: func(ctx context.Context, s *schema.Builder) error {
{{- if .Create}}
			return s.Create(ctx, "{{.Table}}", func(t *schema.Blueprint) {
				t.ID()
				t.Timestamps()
			})
{{- else if .Table}}
			return s.Table(ctx, "{{.Table}}", func(t *schema.Blueprint) {
			})
{{- else}}
			return nil
{{- end}}
		},
		Down: func(ctx context.Context, s *schema.Builder) error {
{{- if .Create}}
			return s.DropIfExists(ctx, "{{.Table}}")
{{- else if .Table}}
			return s.Table(ctx, "{{.Table}}", func(t *schema.Blueprint) {
			})
{{- else}}
			return nil
{{- end}}
		},
	})
}
`))
)

// MakeMigration writes a migration skeleton (make:migration), guessing the
// table from names like create_flights_table or add_votes_to_users_table.
func MakeMigration(dir, pkg, name string, at time.Time) (string, error) {
	name = strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	full := at.Format("2006_01_02_150405") + "_" + name
	data := map[string]any{"Package": pkg, "Name": full}
	if m := createRe.FindStringSubmatch(name); m != nil {
		data["Create"], data["Table"] = true, m[1]
	} else if m := alterRe.FindStringSubmatch(name); m != nil {
		data["Table"] = m[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, full+".go")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return path, makeTmpl.Execute(f, data)
}
