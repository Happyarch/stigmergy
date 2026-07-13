package hostcfg

import (
	"errors"
	"os"
	"strings"
)

// Marker delimiters. Everything between them belongs to stigmergy and is
// regenerated on install; everything outside them is the user's, and is never
// touched. This is what makes an instruction file safe to share with a tool
// that rewrites part of it.
const (
	BeginMarker = "<!-- stigmergy:begin — managed block, do not edit; `stigmergy init` regenerates it -->"
	EndMarker   = "<!-- stigmergy:end -->"
)

// WriteMarkerBlock inserts or replaces stigmergy's block in a markdown file,
// appending to whatever the user already wrote.
func WriteMarkerBlock(path, content string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	block := BeginMarker + "\n" + strings.TrimRight(content, "\n") + "\n" + EndMarker

	before, after, found := splitBlock(string(existing))
	var out string
	switch {
	case found:
		out = before + block + after
	case len(existing) == 0:
		out = block + "\n"
	default:
		out = strings.TrimRight(string(existing), "\n") + "\n\n" + block + "\n"
	}
	return writeFileAtomic(path, []byte(out), 0o644)
}

// RemoveMarkerBlock takes our block back out, leaving the rest of the file
// alone. A file that held nothing but our block is removed entirely.
func RemoveMarkerBlock(path string) error {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	before, after, found := splitBlock(string(existing))
	if !found {
		return nil
	}
	rest := strings.TrimSpace(before + after)
	if rest == "" {
		return removeIfExists(path)
	}
	return writeFileAtomic(path, []byte(rest+"\n"), 0o644)
}

// HasMarkerBlock reports whether a file already carries our block.
func HasMarkerBlock(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, _, found := splitBlock(string(data))
	return found
}

// splitBlock finds our block and returns what surrounds it. An unterminated
// begin marker is treated as "not found", so a half-deleted block is left for a
// human to look at rather than being silently swallowed along with whatever
// followed it.
func splitBlock(s string) (before, after string, found bool) {
	i := strings.Index(s, BeginMarker)
	if i < 0 {
		return s, "", false
	}
	j := strings.Index(s[i:], EndMarker)
	if j < 0 {
		return s, "", false
	}
	end := i + j + len(EndMarker)
	return s[:i], s[end:], true
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
