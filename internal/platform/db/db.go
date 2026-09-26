// Package db owns the Postgres connection pool and the transaction helpers.
// A repository runs its own multi-statement work through InTx. A unit of
// work across modules, such as an order and its first payment, runs through
// Tx: every repository reaching the database through Conn(ctx) then shares
// one transaction, without any module importing another's postgres package.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
)

// DB wraps the connection pool shared by all modules.
type DB struct {
	pool *pgxpool.Pool
}

// Option configures Open.
type Option func(*pgxpool.Config)

// WithTracerProvider records a span for every query, batch, and connection
// acquire that runs inside a traced request or job; queries outside any span
// (startup) are not traced. The SQL text is recorded (it only holds $n
// placeholders); the parameter values are not.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(cfg *pgxpool.Config) {
		cfg.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithTracerProvider(tp))
	}
}

// Open creates the connection pool. Connections are made lazily, so Open
// succeeds while the database is down; /readyz reports reachability.
func Open(ctx context.Context, url string, opts ...Option) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parse error can echo the URL, password included; keep it out of logs.
		return nil, errors.New("parse DATABASE_URL: invalid connection string")
	}
	for _, opt := range opts {
		opt(cfg)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Pool exposes the pool for sqlc-generated queries outside a transaction.
func (d *DB) Pool() *pgxpool.Pool {
	return d.pool
}

// Ping checks that the database accepts connections.
func (d *DB) Ping(ctx context.Context) error {
	return d.pool.Ping(ctx)
}

// Close waits for acquired connections to be released, then closes the pool.
func (d *DB) Close() {
	d.pool.Close()
}

// InTx runs fn in a transaction. It commits when fn returns nil and rolls
// back when fn returns an error or panics. Inside a Tx it runs in a
// savepoint of that transaction, so it commits or rolls back with it.
func (d *DB) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return pgx.BeginFunc(ctx, tx, fn)
	}
	return pgx.BeginFunc(ctx, d.pool, fn)
}

type txKey struct{}

// Querier runs SQL on the pool or in a transaction; it is the DBTX that
// sqlc-generated queries take.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tx runs fn in one transaction that every repository reached with the
// ctx it gets shares, through Conn. It commits when fn returns nil and rolls
// back when fn returns an error or panics. A Tx inside a Tx joins the outer
// transaction as a savepoint.
func (d *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	var begin interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	} = d.pool
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		begin = tx
	}
	return pgx.BeginFunc(ctx, begin, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// Conn returns the transaction ctx carries from Tx, or else the pool.
func (d *DB) Conn(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return d.pool
}
