# Run the suite against SQLite (no setup needed) or the databases from
# compose.yaml (`docker compose up -d --wait` first).

PG_DSN      = postgres://eloquent:eloquent@127.0.0.1:54329/eloquent?sslmode=disable
PG_AUDIT    = postgres://eloquent:eloquent@127.0.0.1:54329/eloquent_audit?sslmode=disable
MYSQL_DSN   = eloquent:eloquent@tcp(127.0.0.1:33069)/eloquent?parseTime=true&multiStatements=true
MYSQL_AUDIT = eloquent:eloquent@tcp(127.0.0.1:33069)/eloquent_audit?parseTime=true
MARIA_DSN   = eloquent:eloquent@tcp(127.0.0.1:33070)/eloquent?parseTime=true&multiStatements=true
MARIA_AUDIT = eloquent:eloquent@tcp(127.0.0.1:33070)/eloquent_audit?parseTime=true

.PHONY: test test-postgres test-mysql test-mariadb test-all generate

test:
	go test ./...

test-postgres:
	ELOQUENT_TEST_DRIVER=pgx ELOQUENT_TEST_DSN='$(PG_DSN)' ELOQUENT_TEST_AUDIT_DSN='$(PG_AUDIT)' go test -count=1 -p 1 ./...

test-mysql:
	ELOQUENT_TEST_DRIVER=mysql ELOQUENT_TEST_DSN='$(MYSQL_DSN)' ELOQUENT_TEST_AUDIT_DSN='$(MYSQL_AUDIT)' go test -count=1 -p 1 ./...

test-mariadb:
	ELOQUENT_TEST_DRIVER=mysql ELOQUENT_TEST_DSN='$(MARIA_DSN)' ELOQUENT_TEST_AUDIT_DSN='$(MARIA_AUDIT)' go test -count=1 -p 1 ./...

test-all: test test-postgres test-mysql test-mariadb

generate:
	go generate ./...
