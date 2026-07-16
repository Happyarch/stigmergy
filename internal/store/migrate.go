package store

import (
	"context"
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
	var pending []migration
	for _, m := range ms {
		if m.version > current {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		// The overwhelmingly common case: an up-to-date database, opened. It
		// must cost nothing, and in particular must not touch the pragmas an
		// established connection is already running under.
		return nil
	}
	return d.applyPending(pending)
}

// applyPending runs the outstanding migrations on a single pinned connection.
//
// The pinning is the point. A table rebuild — the only way SQLite can change a
// CHECK constraint — needs legacy_alter_table=ON, so that renaming a table to
// get it out of the way does not rewrite every child table's REFERENCES clause
// to follow it; and it needs foreign_keys=OFF, so that dropping the old table
// is not itself a violation. Both are connection state, and setting them on
// *sql.DB only reaches whichever connection the pool happens to hand out.
// SetMaxOpenConns(1) makes that one connection in practice, but "in practice"
// is not a guarantee the schema should rest on, and a pragma that silently
// applied to a different connection than the transaction would corrupt every
// foreign key in the database rather than fail.
func (d *DB) applyPending(pending []migration) (err error) {
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = ON`); err != nil {
		return err
	}
	defer func() {
		// Restore the connection to the state the DSN established, so it is
		// safe to hand back to the pool whether or not the migration worked.
		_, _ = conn.ExecContext(ctx, `PRAGMA legacy_alter_table = OFF`)
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	}()

	for _, m := range pending {
		tx, err := conn.BeginTx(ctx, nil)
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
