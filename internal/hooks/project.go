package hooks

import (
	"errors"
	"fmt"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
)

// access is how a hook intends to use the project database.
type access int

const (
	// readOnly is for hooks that only decide — the claim guard, the roster
	// summary shown at session start. It can neither create nor migrate.
	readOnly access = iota
	// writable is for hooks that must record something: a heartbeat, a
	// notified_at stamp, an audit row. It still never migrates.
	writable
)

// opened is a hook's opened view of the project governing a working directory.
type opened struct {
	DB      *store.DB
	Project *project.Project
	// Repo is the git repository the resolution started from. Kept because most
	// hooks only ever care about the one they are standing in.
	Repo *gitx.Repo
}

// unavailable says why a project could not be opened, when the answer is
// something other than "there is no project here".
type unavailable struct {
	// Reason reads as the middle of a sentence, because that is where the claim
	// guard puts it: "claim verification is unavailable (<Reason>)".
	Reason string
	Err    error
}

// resolveProject opens the project database governing cwd.
//
// Three outcomes, and keeping them apart is the entire reason this is one
// function rather than the seven near-copies it replaces:
//
//   - p != nil                 a project is here and this binary understands it
//   - p == nil, why == nil     nothing to govern: not a git repository, or
//     stigmergy was never enabled here
//   - p == nil, why != nil     a project IS here and could not be verified
//
// Only the claim guard acts on the third case, and it fails closed: it knows
// stigmergy is meant to be active and cannot check claims, so allowing the edit
// would risk silently destroying another agent's work. Every other hook treats
// it exactly like the second and stays quiet — a mailbox that cannot open the
// database has nothing useful to say, and saying it on every turn would be noise
// pointing at a problem the guard is already reporting loudly.
//
// It never migrates, whatever the access mode. These hooks run in every project
// an agent touches, on every edit and every turn; a hook that migrated would
// silently upgrade the schema of every repository an agent wandered into,
// mid-edit, under a 250ms lock timeout, racing whatever else holds the database
// open. Schema changes belong to `init`, `doctor` and the MCP server, which can
// afford to take their time and can say what they did.
func resolveProject(cwd string, mode access) (*opened, *unavailable) {
	proj, err := project.Resolve(cwd)
	if errors.Is(err, project.ErrNotARepo) {
		// stigmergy coordinates git repositories and has no opinion about
		// anything else.
		return nil, nil
	}
	if err != nil {
		// A pointer file that exists and cannot be read is a different thing
		// entirely: somebody stated this repository is part of a project, and we
		// cannot tell which. That is corruption, not absence.
		return nil, &unavailable{"this repository's project could not be resolved", err}
	}
	if !proj.Adopted() {
		// stigmergy was never enabled here. A hook installed user-wide fires in
		// every repository, and one that is not participating must not be slowed,
		// blocked, or quietly given a database.
		//
		// Unless a pointer says otherwise: that is an explicit statement that this
		// repository belongs to a project, so a missing database behind it is a
		// fault to report rather than a project opting out.
		if proj.MultiRepo() {
			return nil, &unavailable{
				"the project database named by this repository is missing", nil}
		}
		return nil, nil
	}

	opts := []store.Option{store.BusyTimeout(HookBusyTimeout)}
	if mode == readOnly {
		opts = append(opts, store.ReadOnly())
	} else {
		opts = append(opts, store.NoMigrate())
	}
	db, err := store.OpenProjectAt(proj.DBPath, opts...)
	if err != nil {
		return nil, &unavailable{"the project database could not be opened", err}
	}

	version, err := db.SchemaVersion()
	if err != nil {
		db.Close()
		return nil, &unavailable{"the project database schema could not be read", err}
	}
	latest, err := store.LatestVersion(store.Project)
	if err != nil || version != latest {
		// A database from a newer stigmergy may express claims in ways this
		// binary cannot read, and silently allowing the edit would mean silently
		// ignoring claims that do exist.
		db.Close()
		return nil, &unavailable{
			fmt.Sprintf("the project database is at schema version %d but this stigmergy expects %d",
				version, latest),
			err,
		}
	}
	// The member roster, for the callers that map paths to repositories. One
	// small query on a connection already open, and only reached once a project
	// is known to exist.
	if err := proj.Load(db); err != nil {
		db.Close()
		return nil, &unavailable{"the project's repositories could not be read", err}
	}
	return &opened{DB: db, Project: proj, Repo: proj.Repo}, nil
}

// openQuietly is resolveProject for the hooks that have nothing to say when a
// project cannot be opened. The bool is retained because it reads better at the
// call sites than a nil check on a struct pointer.
func openQuietly(cwd string, mode access) (*opened, bool) {
	p, _ := resolveProject(cwd, mode)
	return p, p != nil
}
