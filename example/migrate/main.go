// Command migrate is the example project's artisan-style migration CLI:
//
//	go run ./example/migrate                 # migrate
//	go run ./example/migrate status
//	go run ./example/migrate rollback -steps 1
//	go run ./example/migrate fresh
//	go run ./example/migrate make create_flights_table -dir example/migrations
//
// It uses SQLite files by default; set DB_DRIVER, DB_DSN and DB_AUDIT_DSN
// to target Postgres ("pgx") or MySQL ("mysql").
package main

import (
	"cmp"
	"context"
	"log"
	"os"

	_ "github.com/stubbedev/eloquent-go/example/migrations"
	"github.com/stubbedev/eloquent-go/orm"
	"github.com/stubbedev/eloquent-go/schema"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func main() {
	driver := cmp.Or(os.Getenv("DB_DRIVER"), "sqlite")
	if _, err := orm.Open(orm.DefaultConnection, driver, cmp.Or(os.Getenv("DB_DSN"), "file:example.db?_time_format=sqlite")); err != nil {
		log.Fatal(err)
	}
	if _, err := orm.Open("audit", driver, cmp.Or(os.Getenv("DB_AUDIT_DSN"), "file:example_audit.db?_time_format=sqlite")); err != nil {
		log.Fatal(err)
	}
	if err := schema.Run(context.Background(), schema.NewMigrator(), os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}
