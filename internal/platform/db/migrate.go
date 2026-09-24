package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Zefanrakh/kue-preorder/db/migrations"
)

// NewMigrator returns a goose provider over the embedded migrations with its
// own connection. The caller must Close it.
func NewMigrator(databaseURL string) (*goose.Provider, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		// The parse error can echo the URL, password included; keep it out of logs.
		return nil, errors.New("parse database url: invalid connection string")
	}
	sqlDB := stdlib.OpenDB(*cfg)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return provider, nil
}

// Migrate applies every pending migration.
func Migrate(ctx context.Context, databaseURL string) error {
	provider, err := NewMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer provider.Close()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
