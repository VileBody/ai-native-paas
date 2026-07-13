package kernelpostgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q has no numeric prefix", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %q has invalid version", entry.Name())
		}
		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(content)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     entry.Name(),
			SQL:      string(content),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for i := 1; i < len(migrations); i++ {
		if migrations[i-1].Version == migrations[i].Version {
			return nil, fmt.Errorf("duplicate migration version %d", migrations[i].Version)
		}
	}
	return migrations, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS kernel`); err != nil {
		return fmt.Errorf("create kernel schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS kernel.schema_migrations (
            version     integer PRIMARY KEY,
            name        text NOT NULL,
            checksum    text NOT NULL,
            applied_at  timestamptz NOT NULL DEFAULT now()
        )`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if err := applyMigration(ctx, db, migration); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.Version, err)
	}
	defer func() { _ = tx.Rollback() }()

	var checksum string
	err = tx.QueryRowContext(ctx, `SELECT checksum FROM kernel.schema_migrations WHERE version = $1`, migration.Version).Scan(&checksum)
	switch {
	case err == nil:
		if checksum != migration.Checksum {
			return fmt.Errorf("migration %d checksum changed", migration.Version)
		}
		return tx.Commit()
	case err != sql.ErrNoRows:
		return fmt.Errorf("inspect migration %d: %w", migration.Version, err)
	}

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kernel.schema_migrations(version, name, checksum) VALUES ($1, $2, $3)`,
		migration.Version, migration.Name, migration.Checksum,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.Version, err)
	}
	return nil
}

func CurrentMigrationVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM kernel.schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}
