// Package postgresbootstrap owns the common production database bootstrap path.
package postgresbootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultMaxOpenConnections = 20
	defaultMaxIdleConnections = 5
	defaultConnectionLifetime = 30 * time.Minute
)

// Open connects to PostgreSQL and verifies the connection before returning it.
// Pool defaults intentionally leave one connection available while a dedicated
// session holds the migration advisory lock.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(defaultMaxOpenConnections)
	db.SetMaxIdleConns(defaultMaxIdleConnections)
	db.SetConnMaxLifetime(defaultConnectionLifetime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return db, nil
}

// WithMigrationLock serializes a component's migrations across every replica.
// PostgreSQL advisory locks are session scoped, so the lock is held on a
// dedicated connection while the migration callback uses the regular pool.
func WithMigrationLock(ctx context.Context, db *sql.DB, component string, migrate func(context.Context) error) (err error) {
	if db == nil {
		return errors.New("database is required")
	}
	component = strings.TrimSpace(component)
	if component == "" {
		return errors.New("migration component is required")
	}
	if migrate == nil {
		return errors.New("migration callback is required")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()

	lockID := advisoryLockID("ai-native-paas:migrations:" + component)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("lock %s migrations: %w", component, err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&unlocked)
		if unlockErr != nil && err == nil {
			err = fmt.Errorf("unlock %s migrations: %w", component, unlockErr)
		} else if !unlocked && err == nil {
			err = fmt.Errorf("unlock %s migrations: lock was not held", component)
		}
	}()

	if err := migrate(ctx); err != nil {
		return fmt.Errorf("migrate %s: %w", component, err)
	}
	return nil
}

func advisoryLockID(value string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return int64(hash.Sum64())
}
