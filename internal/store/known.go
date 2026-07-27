package store

import (
	"errors"

	"github.com/happyarch/stigmergy/internal/serr"
)

// KnownProject is a project database this machine has been told about.
//
// Label is for people, not for lookup: a worktree root today, a project name
// once a project can span several repositories. Only DBPath identifies.
type KnownProject struct {
	DBPath    string `json:"db_path"`
	Label     string `json:"label"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// ErrNotGlobal reports a registry call made against the wrong database.
//
// The registry lives in the global database because it spans projects; asking a
// project database for the list of projects is a category error that would
// otherwise fail later, as a missing table, somewhere less obvious.
var ErrNotGlobal = errors.New("store: the project registry lives in the global database")

// RememberProject records a project database, or refreshes one already known.
//
// Callers are the paths where a human or an agent has just demonstrated that
// this project is real and in use — `init`, `doctor`, and opening a project over
// MCP. It is deliberately never called from the hook path: hooks run on every
// edit under a 250ms lock budget and open the project read-only, and a registry
// write there would put a second database in the critical section of the thing
// the whole design bends around keeping fast.
//
// Best-effort by contract. A failure to record a project must never fail the
// operation that was actually asked for — the registry is a convenience for
// finding projects later, and a missing row costs one manual `doctor`.
func (d *DB) RememberProject(dbPath, label string) error {
	if d.Kind != Global {
		return ErrNotGlobal
	}
	if dbPath == "" {
		return serr.E(serr.InvalidInput, "a project database path is required")
	}
	now := Now()
	// first_seen is preserved on conflict: it records when this machine first
	// met the project, and an UPSERT that overwrote it would quietly turn the
	// registry into a list of "last opened" with a misleading column name.
	_, err := d.Exec(
		`INSERT INTO known_projects(db_path, label, first_seen, last_seen)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(db_path) DO UPDATE SET label = excluded.label, last_seen = excluded.last_seen`,
		dbPath, label, now, now)
	if err != nil {
		return serr.Internalf(err, "failed to record the project")
	}
	return nil
}

// Label is what this project is called in the registry: its own name when it has
// one, and the given fallback otherwise.
//
// It lives here, beside RememberProject, because every caller of that is a
// caller of this. Without it each one passed whatever it happened to know, so a
// project spanning several repositories was named after whichever member most
// recently ran doctor — a name that changed depending on where you were standing.
func (d *DB) Label(fallback string) string {
	if n := d.Name(); n != "" {
		return n
	}
	return fallback
}

// KnownProjects lists every project database this machine has been told about,
// most recently seen first.
func (d *DB) KnownProjects() ([]KnownProject, error) {
	if d.Kind != Global {
		return nil, ErrNotGlobal
	}
	rows, err := d.Query(
		`SELECT db_path, label, first_seen, last_seen FROM known_projects
		  ORDER BY last_seen DESC, db_path`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list known projects")
	}
	defer rows.Close()

	out := []KnownProject{}
	for rows.Next() {
		var p KnownProject
		if err := rows.Scan(&p.DBPath, &p.Label, &p.FirstSeen, &p.LastSeen); err != nil {
			return nil, serr.Internalf(err, "failed to read a known project")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ForgetProject drops a project from the registry. Removing the row does not
// touch the database it names.
func (d *DB) ForgetProject(dbPath string) error {
	if d.Kind != Global {
		return ErrNotGlobal
	}
	if _, err := d.Exec(`DELETE FROM known_projects WHERE db_path = ?`, dbPath); err != nil {
		return serr.Internalf(err, "failed to forget the project")
	}
	return nil
}
