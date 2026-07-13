// Package store owns all SQLite access for both stigmergy databases: the
// per-repository project DB living in the git common dir, and the machine-wide
// global DB under the XDG data dir.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Kind distinguishes the two database schemas.
type Kind string

const (
	Project Kind = "project"
	Global  Kind = "global"
)

// ProjectDBName is the filename of the project database inside the git common dir.
const ProjectDBName = "stigmergy.sqlite3"

// DB wraps a database handle with its identity.
type DB struct {
	*sql.DB
	Kind Kind
	Path string
}

type options struct {
	readOnly    bool
	busyTimeout int
}

// Option adjusts how a database is opened.
type Option func(*options)

// ReadOnly opens the database without creating or migrating it. Used by the
// hook fast path, which must never mutate schema.
func ReadOnly() Option { return func(o *options) { o.readOnly = true } }

// BusyTimeout overrides the default 5000ms lock wait. The hook path uses 250ms.
func BusyTimeout(ms int) Option { return func(o *options) { o.busyTimeout = ms } }

// ProjectDBPath returns where the project database lives for a git common dir.
func ProjectDBPath(commonDir string) string {
	return filepath.Join(commonDir, ProjectDBName)
}

// OpenProject opens (creating and migrating unless ReadOnly) the project
// database inside the given git common directory.
func OpenProject(commonDir string, opts ...Option) (*DB, error) {
	meta := map[string]string{
		"db_kind":            string(Project),
		"created_common_dir": commonDir,
	}
	return open(ProjectDBPath(commonDir), Project, meta, opts...)
}

// OpenGlobal opens (creating and migrating unless ReadOnly) the global
// database at the given path.
func OpenGlobal(path string, opts ...Option) (*DB, error) {
	return open(path, Global, map[string]string{"db_kind": string(Global)}, opts...)
}

func open(path string, kind Kind, meta map[string]string, opts ...Option) (*DB, error) {
	o := options{busyTimeout: 5000}
	for _, f := range opts {
		f(&o)
	}
	if o.readOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn(path, o))
	if err != nil {
		return nil, err
	}
	// One connection avoids in-process lock contention; cross-process
	// concurrency is WAL's job. Throughput needs here are trivial.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	d := &DB{DB: db, Kind: kind, Path: path}
	if !o.readOnly {
		if err := d.migrate(); err != nil {
			db.Close()
			return nil, err
		}
		if err := d.ensureMeta(meta); err != nil {
			db.Close()
			return nil, err
		}
	}
	return d, nil
}

func dsn(path string, o options) string {
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath()}
	q := url.Values{}
	if o.readOnly {
		q.Add("mode", "ro")
	} else {
		// Every write path in stigmergy reads-then-writes inside one tx (CAS,
		// claim overlap). A deferred tx would take a read lock and fail to
		// upgrade with SQLITE_BUSY_SNAPSHOT, which the busy handler will not
		// retry. Taking the write lock up front is what makes the busy_timeout
		// actually cover concurrent roots.
		q.Add("_txlock", "immediate")
	}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", o.busyTimeout))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	return u.String() + "?" + q.Encode()
}

func (d *DB) ensureMeta(meta map[string]string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		rows[k] = v
	}
	rows["created_at"] = Now()
	for k, v := range rows {
		if _, err := tx.Exec(
			`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, k, v,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TimeLayout is the one timestamp format in the databases.
//
// It is deliberately NOT time.RFC3339Nano: that layout strips trailing zeros
// from the fractional seconds, so its output is not fixed-width, and string
// comparison stops matching chronological order ("…:00.5Z" sorts before
// "…:00Z" because '.' < 'Z'). Every TTL and expiry predicate compares these
// timestamps as TEXT in SQL, so fixed-width UTC is load-bearing.
const TimeLayout = "2006-01-02T15:04:05.000000000Z"

// nowFn is swappable so TTL and expiry behavior can be tested without sleeping.
var nowFn = time.Now

// SetClock replaces the clock and returns a function restoring the real one.
// Test-only, but exported because the tests that need it live in other packages.
func SetClock(f func() time.Time) (restore func()) {
	prev := nowFn
	nowFn = f
	return func() { nowFn = prev }
}

// NowTime returns the current UTC time from the (possibly faked) clock.
func NowTime() time.Time { return nowFn().UTC() }

// Now returns the server-generated UTC timestamp used everywhere. Agents never
// supply timestamps.
func Now() string { return Stamp(NowTime()) }

// Stamp formats a time in the canonical storage layout.
func Stamp(t time.Time) string { return t.UTC().Format(TimeLayout) }

// ParseStamp reads a stored timestamp back.
func ParseStamp(s string) (time.Time, error) { return time.Parse(TimeLayout, s) }
