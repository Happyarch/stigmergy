package importer

import (
	"os"
	"path/filepath"
	"regexp"
)

// LegacyDirs returns the places a Claude Code project keeps markdown memories.
//
// The project-scoped path under ~/.claude/projects/<slug>/memory is the one
// that matters: those memories are about *this* repository and belong in the
// project scope. A user-wide memory directory is left alone — it is shared
// across every project, and hoovering it into one repository's database would
// be both wrong and surprising.
func LegacyDirs(worktree string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	candidates := []string{
		filepath.Join(home, ".claude", "projects", ProjectSlug(worktree), "memory"),
		filepath.Join(worktree, ".claude", "memory"),
	}
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// nonAlnum matches everything Claude Code flattens when it names a project's
// state directory.
var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// ProjectSlug is how Claude Code names a project's state directory: the
// absolute path with every non-alphanumeric character flattened to a hyphen,
// preserving case. So "/mnt/sdb1/Code/Active Code/repo" becomes
// "-mnt-sdb1-Code-Active-Code-repo" — note that the space is flattened too, not
// just the separators.
func ProjectSlug(worktree string) string {
	return nonAlnum.ReplaceAllString(worktree, "-")
}

// Count reports how many importable files a directory holds, for the prompt
// that asks whether to import them.
func Count(dir string) int {
	files, err := Discover(dir)
	if err != nil {
		return 0
	}
	return len(files)
}
