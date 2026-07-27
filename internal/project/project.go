// Package project resolves which stigmergy project governs a directory, and
// which of its repositories a given file belongs to.
//
// A project used to be a repository, exactly and only: the database lived at
// <git-common-dir>/stigmergy.sqlite3, so its location WAS the project's
// identity. That is still true for a project with one repository, and this
// package resolves those without touching anything new. But a project can now
// span several repositories — a client and its service, with separate remotes,
// whose changes routinely cross both — and those keep one shared database
// outside all of them, with each member's git common dir holding a pointer to it.
//
// Resolution is filesystem-only, and that is a hard constraint rather than an
// implementation detail. The claim guard runs as a fresh process on every single
// Edit and Write, inside a latency budget measured in milliseconds, and it
// resolves a project before it can decide anything. So: no subprocess, no
// database query, no directory walk beyond the one gitx already does.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

// PointerName is the file in a git common dir that says which project a
// repository belongs to. It sits beside where a single-repo database would live,
// and inside .git/, so it is never committed and needs no gitignore entry.
const PointerName = "stigmergy-project.json"

// ErrNotARepo reports a directory that is not inside a git repository, so there
// is no project to resolve and nowhere to put one.
//
// Note this is the ONLY thing resolution refuses. A git repository that has
// never run `stigmergy init` still resolves, to the place its database would go
// — because the two questions "where would this project's state live?" and "is
// stigmergy enabled here?" have different callers. context_open answers the
// first and then creates the database, which is how a project is adopted from
// inside an agent session. Every hook answers the second, via Adopted, and stays
// out of the way when it is false. Collapsing them would mean either hooks
// creating databases in every repository an agent wanders into, or agents
// unable to adopt a project at all.
var ErrNotARepo = errors.New("project: not inside a git repository")

// memberIDRe is the grammar for a member's name.
//
// It is deliberately the same shape as a memory key, and for the same reason:
// agents type these. A member id is the prefix in a "repo:path" claim scope, so
// it has to be short, unambiguous, and free of anything that would need quoting
// or escaping — and in particular free of ':' and '/', which would make a scope
// impossible to split back apart.
var memberIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidateMemberID checks a proposed member name.
func ValidateMemberID(id string) error {
	if !memberIDRe.MatchString(id) {
		return fmt.Errorf("repository name %q is invalid: lowercase letters, digits and hyphens, "+
			"starting with a letter or digit, up to 64 characters", id)
	}
	return nil
}

// SlugFor proposes a member name from a worktree path: the directory's own name,
// reduced to the grammar above.
//
// A proposal, not a decision — `stigmergy project add` lets it be overridden,
// because two repositories can perfectly well sit in directories with the same
// name and only a person knows what to call them apart. It exists so the common
// case needs no argument at all.
func SlugFor(worktree string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(worktree)))
	var b strings.Builder
	lastHyphen := false
	for _, r := range base {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case b.Len() > 0 && !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	if slug == "" {
		// Everything about the directory name was unusable. "repo" is a poor name
		// and a working one; the alternative is failing adoption over a path.
		return "repo"
	}
	return slug
}

// Member is one repository of a project.
type Member struct {
	ID           string `json:"repo"`
	WorktreeRoot string `json:"worktree"`
	CommonDir    string `json:"common_dir"`
}

// Pointer is the contents of a member's pointer file.
type Pointer struct {
	// ProjectID is opaque and machine-generated: not a slug and not a path, so
	// it is symmetric between members, survives a directory being renamed or
	// moved, and cannot collide with someone else's project of the same name.
	ProjectID string `json:"project"`
	// RepoID is this member's name within the project.
	RepoID    string `json:"repo"`
	CreatedAt string `json:"created_at"`
}

// Project is the resolved answer to "what governs this directory?".
type Project struct {
	// ID is "" for a single-repository project, which has no pointer file and
	// keeps its database in its own git common dir. That is not a degenerate
	// case to be tidied away later — it is the shape every existing project has,
	// and it must keep working untouched.
	ID string
	// DBPath is where the project database lives, whichever shape this is.
	DBPath string
	// SelfID is the member the resolved directory belongs to.
	SelfID string
	// Repo is the git repository the resolution started from.
	Repo *gitx.Repo
	// Members is empty until Load reads them from the database. Resolution does
	// not need them, and the hook path that only wants to open the right
	// database should not pay for a query it will not use.
	Members []Member
}

// MultiRepo reports whether this project spans more than one repository.
//
// It keys on the pointer, not on len(Members), so it answers correctly before
// Load has run — and so a project deliberately created with one member still
// reads as multi-repo, because it is one that a second repository can join.
func (p *Project) MultiRepo() bool { return p.ID != "" }

// StateDir is where a multi-repo project keeps everything that is not the
// database: deliberation runs, and whatever else comes later.
func StateDir(projectID string) (string, error) {
	base, err := xdg.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "projects", projectID), nil
}

// DBPathFor returns where a multi-repo project's database lives.
func DBPathFor(projectID string) (string, error) {
	dir, err := StateDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, store.ProjectDBName), nil
}

// PointerPath returns a repository's pointer file path.
func PointerPath(commonDir string) string {
	return filepath.Join(commonDir, PointerName)
}

// ReadPointer reads a repository's pointer, or reports that it has none.
//
// A missing file is the ordinary case, not an error: it is how a single-repo
// project is spelled.
func ReadPointer(commonDir string) (*Pointer, bool, error) {
	b, err := os.ReadFile(PointerPath(commonDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var p Pointer
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, false, fmt.Errorf("project: %s is not readable: %w", PointerPath(commonDir), err)
	}
	if p.ProjectID == "" || p.RepoID == "" {
		return nil, false, fmt.Errorf("project: %s names no project", PointerPath(commonDir))
	}
	return &p, true, nil
}

// WritePointer records a repository's membership.
func WritePointer(commonDir string, p Pointer) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PointerPath(commonDir), append(b, '\n'), 0o644)
}

// RemovePointer drops a repository's membership record. A missing file is not
// an error — removing a member twice should not fail the second time.
func RemovePointer(commonDir string) error {
	err := os.Remove(PointerPath(commonDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Resolve finds the project governing dir.
//
// Filesystem-only: one gitx walk, then at most one small file read. It never
// scans parent directories for a project and never infers membership from
// sibling repositories. A repository belongs to a project because someone ran a
// command that wrote a pointer into it — nothing else, ever. Anything looser
// would eventually adopt a repository the user did not mean to hand over.
func Resolve(dir string) (*Project, error) {
	repo, err := gitx.Resolve(dir)
	if err != nil {
		return nil, ErrNotARepo
	}
	return fromRepo(repo)
}

// Adopted reports whether stigmergy is actually enabled here — whether the
// database this project resolves to exists.
//
// Every hook asks this, and a false answer means "stay out of the way": a hook
// installed user-wide fires in every repository, and one that has not adopted
// stigmergy must not be slowed, blocked, or quietly given a database.
func (p *Project) Adopted() bool {
	_, err := os.Stat(p.DBPath)
	return err == nil
}

// ResolveWithFallback is Resolve with gitx's subprocess fallback. Never use it
// on the hook path; spawning git on every edit breaks the latency budget.
func ResolveWithFallback(dir string) (*Project, error) {
	repo, err := gitx.ResolveWithFallback(dir)
	if err != nil {
		return nil, ErrNotARepo
	}
	return fromRepo(repo)
}

func fromRepo(repo *gitx.Repo) (*Project, error) {
	ptr, ok, err := ReadPointer(repo.CommonDir)
	if err != nil {
		return nil, err
	}
	if ok {
		dbPath, err := DBPathFor(ptr.ProjectID)
		if err != nil {
			return nil, err
		}
		return &Project{ID: ptr.ProjectID, DBPath: dbPath, SelfID: ptr.RepoID, Repo: repo}, nil
	}

	// No pointer: the single-repository shape, unchanged since the beginning.
	// Returned whether or not the database exists yet — see ErrNotARepo.
	return &Project{DBPath: store.ProjectDBPath(repo.CommonDir), Repo: repo}, nil
}

// Load fills in the member roster from the database.
//
// Separate from Resolve because the two have different costs and different
// callers: resolving is a file read and every hook does it, while loading is a
// query and only the callers that must map paths to repositories need it.
func (p *Project) Load(db *store.DB) error {
	repos, err := db.Repos()
	if err != nil {
		return err
	}
	p.Members = p.Members[:0]
	for _, r := range repos {
		p.Members = append(p.Members, Member{
			ID: r.RepoID, WorktreeRoot: r.Worktree, CommonDir: r.CommonDir,
		})
	}
	return nil
}

// Containing returns the member repository an absolute path belongs to, or nil
// if it belongs to none of them.
//
// Longest worktree root wins, because members can nest: a repository checked out
// inside another repository's tree is unusual but legal, and the inner one is
// the right answer for a path inside it. This is antigravity's workspace
// resolution, which had to solve the same problem first and is now shared by
// every host.
//
// Before Load has run there are no members, and a single-repo project answers
// from its own worktree instead — so a caller that never loads still gets the
// right answer for the only repository there is.
func (p *Project) Containing(abs string) *Member {
	if len(p.Members) == 0 {
		if p.Repo != nil && within(p.Repo.WorktreeRoot, abs) {
			return &Member{ID: p.SelfID, WorktreeRoot: p.Repo.WorktreeRoot, CommonDir: p.Repo.CommonDir}
		}
		return nil
	}
	var best *Member
	for i := range p.Members {
		m := &p.Members[i]
		if !within(m.WorktreeRoot, abs) {
			continue
		}
		if best == nil || len(m.WorktreeRoot) > len(best.WorktreeRoot) {
			best = m
		}
	}
	return best
}

// Member returns a member by id.
func (p *Project) Member(id string) *Member {
	for i := range p.Members {
		if p.Members[i].ID == id {
			return &p.Members[i]
		}
	}
	return nil
}

// MemberIDs returns every member's id, for error messages that have to say what
// the valid answers were.
func (p *Project) MemberIDs() []string {
	out := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		out = append(out, m.ID)
	}
	return out
}

// within reports whether abs is root or lives beneath it.
//
// Component-wise, never a raw string prefix — the same rule the claim overlap
// test rests on, and for the same reason. Without the separator, a worktree at
// /code/app would "contain" every path under /code/app-old.
func within(root, abs string) bool {
	if root == "" {
		return false
	}
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	if root == abs {
		return true
	}
	return strings.HasPrefix(abs, root+string(filepath.Separator))
}
