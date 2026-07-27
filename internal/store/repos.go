package store

import (
	"database/sql"
	"errors"

	"github.com/happyarch/stigmergy/internal/serr"
)

// LegacyRepoID is the repo_id carried by claims written before a project knew it
// could have more than one repository, and by every claim in a project that
// still has exactly one.
//
// It is deliberately not a sentinel to be migrated away. A single-repo project
// never needs a name for its only repository, and inventing one would mean every
// existing database had to be rewritten before its claims could be trusted
// again. Instead the empty string means "this project's own repository", and
// selfRepoID resolves it at read time.
const LegacyRepoID = ""

// ErrNoRepo reports a repository that is not a member of this project.
var ErrNoRepo = errors.New("store: no such repository in this project")

// RepoRow is one member repository of a project.
type RepoRow struct {
	RepoID    string `json:"repo"`
	CommonDir string `json:"common_dir"`
	Worktree  string `json:"worktree"`
	AddedAt   string `json:"added_at"`
}

// Repos lists the project's member repositories, in a stable order.
func (d *DB) Repos() ([]RepoRow, error) {
	rows, err := d.Query(
		`SELECT repo_id, common_dir, worktree, added_at FROM repos ORDER BY added_at, repo_id`)
	if err != nil {
		return nil, serr.Internalf(err, "failed to list the project's repositories")
	}
	defer rows.Close()

	out := []RepoRow{}
	for rows.Next() {
		var r RepoRow
		if err := rows.Scan(&r.RepoID, &r.CommonDir, &r.Worktree, &r.AddedAt); err != nil {
			return nil, serr.Internalf(err, "failed to read a repository")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddRepo records a member repository.
//
// common_dir is UNIQUE, so adding the same repository twice is refused rather
// than silently duplicated under a second name — two names for one checkout
// would split its claims in half, and each half would stop blocking the other.
func (d *DB) AddRepo(repoID, commonDir, worktree string) error {
	if repoID == "" {
		return serr.E(serr.InvalidInput, "a repository name is required")
	}
	_, err := d.Exec(
		`INSERT INTO repos(repo_id, common_dir, worktree, added_at) VALUES(?, ?, ?, ?)`,
		repoID, commonDir, worktree, Now())
	if err != nil {
		return serr.E(serr.InvalidInput,
			"repository %q could not be added (is it already a member, under this or another name?): %v",
			repoID, err)
	}
	return nil
}

// RemoveRepo drops a member and releases every claim it held.
//
// Releasing is not optional. A claim scoped to a repository nobody can resolve
// any more would go on blocking edits with no way to negotiate it away: the
// owner cannot release what it can no longer name.
func (d *DB) RemoveRepo(repoID string) error {
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM repos WHERE repo_id = ?`, repoID)
	if err != nil {
		return serr.Internalf(err, "failed to remove the repository")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoRepo
	}
	if _, err := tx.Exec(
		`UPDATE claims SET released_at = ? WHERE repo_id = ? AND released_at IS NULL`,
		Now(), repoID); err != nil {
		return serr.Internalf(err, "failed to release the repository's claims")
	}
	if err := audit(tx, AuditEntry{Action: "repo_remove", Target: repoID}); err != nil {
		return serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit the repository removal")
	}
	return nil
}

// EnsureSelfRepo registers the repository a single-repo project was opened from,
// and adopts the claims still carrying the legacy empty repo_id.
//
// This is the 0007 backfill, and it lives in Go rather than in the migration
// because SQL cannot do it: the repo row needs a worktree root and a common dir,
// and a migration has neither — it sees only the database. So the caller, which
// resolved the repository to get here, passes them in.
//
// SINGLE-REPOSITORY PROJECTS ONLY. Callers must not invoke it on a project with
// a pointer. In a multi-repo project every member is already registered by
// `project create`/`add`, so the insert below could only ever add a spurious
// one — and worse, the UPDATE would sweep every legacy claim onto whichever
// member the caller happened to be standing in, giving a different answer
// depending on where `doctor` was run.
//
// Idempotent, and called only from paths that can afford a write — `init` and
// `doctor`. Never from a hook: hooks open the project read-only or with
// NoMigrate, and a backfill on the edit path is exactly the surprise schema
// write the whole design forbids.
func (d *DB) EnsureSelfRepo(repoID, commonDir, worktree string) error {
	if repoID == "" || commonDir == "" || worktree == "" {
		return serr.E(serr.InvalidInput, "a repository name, common dir and worktree are all required")
	}
	tx, err := d.Begin()
	if err != nil {
		return serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRow(`SELECT repo_id FROM repos WHERE common_dir = ?`, commonDir).Scan(&existing)
	switch {
	case err == nil:
		repoID = existing // already a member, under whatever name it was given
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			`INSERT INTO repos(repo_id, common_dir, worktree, added_at) VALUES(?, ?, ?, ?)`,
			repoID, commonDir, worktree, Now()); err != nil {
			return serr.Internalf(err, "failed to record the project's own repository")
		}
	default:
		return serr.Internalf(err, "failed to look up the project's own repository")
	}

	// Adopt the claims written before there was anything to attribute them to.
	if _, err := tx.Exec(
		`UPDATE claims SET repo_id = ? WHERE repo_id = ?`, repoID, LegacyRepoID); err != nil {
		return serr.Internalf(err, "failed to attribute existing claims")
	}
	if err := tx.Commit(); err != nil {
		return serr.Internalf(err, "failed to commit the repository backfill")
	}
	return nil
}

// MetaNameKey is where a project's human-readable name is kept.
const MetaNameKey = "project_name"

// SetMeta records a project-level fact.
//
// The value goes through the same text rules as anything else an agent or a user
// types, because it comes back out the same way: the project name is what
// `doctor --all` prints for every project on the machine, and what the registry
// stores as a label. It is one line, rendered inline, in a list — the same shape
// as a claim reason, and it gets the same treatment.
func (d *DB) SetMeta(key, value string) error {
	value = NormalizeText(value)
	if err := ValidateLine("value", value, MaxLineLength); err != nil {
		return err
	}
	_, err := d.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return serr.Internalf(err, "failed to record %s", key)
	}
	return nil
}

// Meta reads a project-level fact, or "" if it was never set.
func (d *DB) Meta(key string) string {
	var v string
	if err := d.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// Name is what to call this project in a listing.
//
// It exists because the registry's label was whatever the LAST caller happened
// to pass, so a project spanning several repositories ended up named after
// whichever member most recently ran doctor — a name that changes depending on
// where you were standing when you last looked at it.
func (d *DB) Name() string { return d.Meta(MetaNameKey) }
