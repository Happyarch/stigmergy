// Package gitx resolves Git worktree roots and common directories without
// spawning git, keeping the hook fast path inside its latency budget. A
// subprocess fallback exists for callers that are not latency-critical.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo locates a worktree and the common directory shared by every linked
// worktree of its repository. The common directory is where the project
// database lives, so all worktrees of one repo share state while separate
// clones stay isolated.
type Repo struct {
	WorktreeRoot string
	CommonDir    string
}

// ErrNotARepo reports that the starting directory is not inside a Git worktree.
var ErrNotARepo = errors.New("gitx: not inside a git worktree")

// Resolve walks up from dir to the nearest worktree root and derives the git
// common directory using only filesystem reads.
func Resolve(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for cur := abs; ; {
		gitPath := filepath.Join(cur, ".git")
		fi, statErr := os.Lstat(gitPath)
		if statErr == nil {
			gitDir, err := gitDirFrom(cur, gitPath, fi.IsDir())
			if err != nil {
				return nil, err
			}
			common, err := commonDirFrom(gitDir)
			if err != nil {
				return nil, err
			}
			return &Repo{WorktreeRoot: cur, CommonDir: common}, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, ErrNotARepo
		}
		cur = parent
	}
}

// gitDirFrom returns the gitdir for a worktree: the .git directory itself, or
// the target of a ".git" pointer file ("gitdir: <path>") for linked worktrees.
func gitDirFrom(worktree, gitPath string, isDir bool) (string, error) {
	if isDir {
		return gitPath, nil
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("gitx: malformed .git pointer file at %s", gitPath)
	}
	p := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	return filepath.Clean(p), nil
}

// commonDirFrom maps a gitdir to the repository's common directory: the
// contents of its "commondir" file when present (linked worktrees), otherwise
// the gitdir itself (the main worktree).
func commonDirFrom(gitDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, os.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		p = filepath.Join(gitDir, p)
	}
	return filepath.Clean(p), nil
}

// ResolveWithFallback resolves via pure Go and falls back to `git -C` when
// that fails. Never use on the hook path; subprocess spawn breaks the budget.
func ResolveWithFallback(dir string) (*Repo, error) {
	if r, err := Resolve(dir); err == nil {
		return r, nil
	}
	return resolveGit(dir)
}

func resolveGit(dir string) (*Repo, error) {
	common, err := gitOut(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, ErrNotARepo
	}
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, ErrNotARepo
	}
	return &Repo{WorktreeRoot: top, CommonDir: common}, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
