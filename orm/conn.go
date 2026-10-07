package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultConnection is the connection used by tables that don't name one.
const DefaultConnection = "default"

// DB is satisfied by *sql.DB, *sql.Tx and *sql.Conn.
type DB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Conn is a registered connection: a handle plus the dialect its SQL is rendered in.
type Conn struct {
	DB      DB
	Dialect Dialect
}

var (
	connMu sync.RWMutex
	conns  = map[string]Conn{}
)

// Open opens a database with a database/sql driver and registers it under
// name, picking the dialect from the driver name:
//
//	orm.Open(orm.DefaultConnection, "sqlite", "file:app.db")
//	orm.Open("analytics", "pgx", os.Getenv("ANALYTICS_DSN"))
func Open(name, driver, dsn string) (*sql.DB, error) {
	d, ok := DialectFor(driver)
	if !ok {
		return nil, fmt.Errorf("orm: no dialect known for driver %q; use AddConnection with an explicit dialect", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	AddConnection(name, db, d)
	return db, nil
}

// SetDefault registers the default connection.
func SetDefault(db DB, d Dialect) { AddConnection(DefaultConnection, db, d) }

// AddConnection registers a named connection, used by tables declared with
// //orm:table <name> connection=<conn> and by Query.On(<conn>).
func AddConnection(name string, db DB, d Dialect) {
	connMu.Lock()
	defer connMu.Unlock()
	conns[name] = Conn{DB: db, Dialect: d}
}

// Transaction runs fn in a transaction on the default connection. Queries
// that receive the ctx passed to fn run inside the transaction; it commits
// if fn returns nil and rolls back otherwise. Nested calls use savepoints.
func Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return TransactionOn(ctx, DefaultConnection, fn)
}

// txState is carried in the ctx of a running transaction.
type txState struct {
	conn  Conn
	depth int
	after *[]func(context.Context) // afterCommit callbacks, shared with nested levels
}

type txKey struct{ conn string }

// TransactionOn is Transaction for a named connection.
func TransactionOn(ctx context.Context, name string, fn func(ctx context.Context) error) (err error) {
	if st, ok := ctx.Value(txKey{name}).(*txState); ok {
		return savepoint(ctx, name, st, fn)
	}
	c, err := lookup(ctx, name)
	if err != nil {
		return err
	}
	beginner, ok := c.DB.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return fmt.Errorf("orm: connection %q cannot begin a transaction", name)
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	st := &txState{conn: Conn{DB: tx, Dialect: c.Dialect}, after: new([]func(context.Context))}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit(); err == nil {
			for _, f := range *st.after {
				f(ctx)
			}
		}
	}()
	return fn(context.WithValue(ctx, txKey{name}, st))
}

// savepoint runs a nested transaction (DB::transaction inside another).
func savepoint(ctx context.Context, name string, parent *txState, fn func(ctx context.Context) error) (err error) {
	sp := fmt.Sprintf("trans%d", parent.depth+2)
	if _, err := parent.conn.DB.ExecContext(ctx, "SAVEPOINT "+sp); err != nil {
		return err
	}
	mark := len(*parent.after)
	child := &txState{conn: parent.conn, depth: parent.depth + 1, after: parent.after}
	rollback := func() {
		parent.conn.DB.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+sp)
		*parent.after = (*parent.after)[:mark]
	}
	defer func() {
		if p := recover(); p != nil {
			rollback()
			panic(p)
		}
	}()
	if err = fn(context.WithValue(ctx, txKey{name}, child)); err != nil {
		rollback()
		return err
	}
	_, err = parent.conn.DB.ExecContext(ctx, "RELEASE SAVEPOINT "+sp)
	return err
}

// AfterCommit runs fn once the outermost transaction on the default
// connection commits, or immediately when ctx carries no transaction.
// Callbacks registered in a rolled back (nested) transaction are discarded.
func AfterCommit(ctx context.Context, fn func(ctx context.Context)) {
	AfterCommitOn(ctx, DefaultConnection, fn)
}

// AfterCommitOn is AfterCommit for a named connection.
func AfterCommitOn(ctx context.Context, conn string, fn func(ctx context.Context)) {
	if st, ok := ctx.Value(txKey{conn}).(*txState); ok {
		*st.after = append(*st.after, fn)
		return
	}
	fn(ctx)
}

// InTransaction reports whether ctx carries a transaction on the default connection.
func InTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{DefaultConnection}).(*txState)
	return ok
}

// QueryEvent describes one executed statement (DB::listen).
type QueryEvent struct {
	Connection string
	SQL        string
	Args       []any
	Duration   time.Duration
	Err        error
}

var (
	listenMu  sync.RWMutex
	listeners = map[int]func(QueryEvent){}
	listenSeq int
)

// Listen registers fn to receive every executed statement; call the
// returned function to stop listening.
func Listen(fn func(QueryEvent)) (stop func()) {
	listenMu.Lock()
	defer listenMu.Unlock()
	listenSeq++
	id := listenSeq
	listeners[id] = fn
	return func() {
		listenMu.Lock()
		defer listenMu.Unlock()
		delete(listeners, id)
	}
}

// Emit reports an executed statement to listeners (used by the schema package too).
func Emit(e QueryEvent) {
	listenMu.RLock()
	defer listenMu.RUnlock()
	for _, fn := range listeners {
		fn(e)
	}
}

// IsUniqueViolation reports whether err is a unique constraint violation
// on SQLite, Postgres or MySQL/MariaDB.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"UNIQUE constraint failed", "SQLSTATE 23505", "Error 1062", "Duplicate entry"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Connection resolves a registered connection by name, returning the active
// transaction instead when ctx carries one for that connection.
func Connection(ctx context.Context, name string) (Conn, error) { return lookup(ctx, name) }

// lookup resolves a connection name: an active transaction in ctx wins,
// then the registry.
func lookup(ctx context.Context, name string) (Conn, error) {
	if st, ok := ctx.Value(txKey{name}).(*txState); ok {
		return st.conn, nil
	}
	connMu.RLock()
	c, ok := conns[name]
	connMu.RUnlock()
	if !ok {
		return Conn{}, fmt.Errorf("orm: no connection %q registered (call orm.Open, orm.SetDefault or orm.AddConnection)", name)
	}
	return c, nil
}

// dialectOf returns the dialect registered for name, or SQLite when the
// connection is not registered yet (so ToSQL works before startup wiring).
func dialectOf(name string) Dialect {
	connMu.RLock()
	defer connMu.RUnlock()
	if c, ok := conns[name]; ok {
		return c.Dialect
	}
	return SQLite
}
