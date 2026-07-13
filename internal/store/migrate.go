package store

import (
	"database/sql"
	"embed"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/project/*.sql migrations/global/*.sql
var migrationsFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(kind Kind) ([]migration, error) {
	dir := path.Join("migrations", string(kind))
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		numStr, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q lacks NNNN_ prefix", name)
		}
		v, err := strconv.Atoi(numStr)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q has non-numeric version: %w", name, err)
		}
		b, err := migrationsFS.ReadFile(path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// LatestVersion reports the newest migration version compiled into this binary
// for a schema kind. The hook path compares it against the on-disk version and
// fails closed on mismatch instead of migrating.
func LatestVersion(kind Kind) (int, error) {
	ms, err := loadMigrations(kind)
	if err != nil || len(ms) == 0 {
		return 0, fmt.Errorf("store: no migrations for kind %s: %w", kind, err)
	}
	return ms[len(ms)-1].version, nil
}

// SchemaVersion reports the highest migration version applied to the database.
func (d *DB) SchemaVersion() (int, error) {
	var v sql.NullInt64
	err := d.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

func (d *DB) migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	current, err := d.SchemaVersion()
	if err != nil {
		return err
	}
	ms, err := loadMigrations(d.Kind)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: applying %s: %w", m.name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			m.version, Now(),
		); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
