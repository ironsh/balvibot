// Package db opens the shared Postgres connection (via the pgx stdlib driver,
// so the store keeps using database/sql) and runs the embedded Goose
// migrations.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ironsh/balvibot/tools/api/migrations"
)

// Open dials Postgres using the given DSN (a libpq/pgx connection string or
// URL, e.g. "postgres://user:pass@host:5432/db?sslmode=disable") and verifies
// the connection with a ping.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("db: DSN is required")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.PingContext(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

func gooseProvider() error {
	goose.SetBaseFS(migrations.FS)
	return goose.SetDialect("postgres")
}

// MigrateUp applies all pending migrations.
func MigrateUp(db *sql.DB) error {
	if err := gooseProvider(); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// MigrateDown rolls back the most recent migration.
func MigrateDown(db *sql.DB) error {
	if err := gooseProvider(); err != nil {
		return err
	}
	return goose.Down(db, ".")
}

// MigrateStatus prints the migration status to stdout.
func MigrateStatus(db *sql.DB) error {
	if err := gooseProvider(); err != nil {
		return err
	}
	return goose.Status(db, ".")
}
