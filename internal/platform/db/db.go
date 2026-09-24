// Package db owns the Postgres connection pool and the transaction helper.
// Modules run multi-statement work through InTx instead of calling Begin.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps the connection pool shared by all modules.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates the connection pool. Connections are made lazily, so Open
// succeeds while the database is down; /readyz reports reachability.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parse error can echo the URL, password included; keep it out of logs.
		return nil, errors.New("parse DATABASE_URL: invalid connection string")
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
// back when fn returns an error or panics.
func (d *DB) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.pool, fn)
}
