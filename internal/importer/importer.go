// Package importer brings an existing Claude Code markdown memory directory
// into stigmergy.
//
// Two rules shape everything here. The source files are never modified or
// deleted — they are the user's notes, and this is a copy, not a migration.
// And an import never overwrites an existing memory: if a key already exists
// with different content, that is a conflict for a human to look at, because
// the alternative is silently destroying whichever version happened to lose.
package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// IndexFile is the memory index, which describes the other files rather than
// holding a memory of its own.
const IndexFile = "MEMORY.md"

// Frontmatter is the YAML header a Claude memory file may carry.
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

// Entry is one memory file, parsed.
type Entry struct {
	SourcePath  string
	Key         string
	Type        string
	Description string
	Body        string
}

// Outcome is what happened to one file.
type Outcome struct {
	SourcePath string `json:"source_path"`
	Key        string `json:"key,omitempty"`
	Status     string `json:"status"` // imported | skipped | conflict | invalid
	Detail     string `json:"detail,omitempty"`
}

// Report is the result of an import.
type Report struct {
	SourceDir string    `json:"source_dir"`
	DryRun    bool      `json:"dry_run"`
	Imported  int       `json:"imported"`
	Skipped   int       `json:"skipped"`
	Conflicts int       `json:"conflicts"`
	Invalid   int       `json:"invalid"`
	Outcomes  []Outcome `json:"outcomes"`
}

var (
	frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)
	slugRe        = regexp.MustCompile(`[^a-z0-9]+`)
)

// validTypes mirrors the schema's CHECK constraint.
var validTypes = map[string]bool{"user": true, "feedback": true, "project": true, "reference": true}

// Discover finds importable memory files. The index is skipped: it is a table
// of contents, not a memory, and importing it would create an entry that goes
// stale the moment anything else changes.
func Discover(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		if strings.EqualFold(filepath.Base(path), IndexFile) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// Parse reads one memory file.
//
// Frontmatter is optional, because these files are hand-written and many will
// not have it. Everything it would supply has a sane fallback: the key from the
// filename, the description from the first line of prose, the type from a
// conservative default. A file is only invalid if it has no content at all.
func Parse(path string) (*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)

	var fm Frontmatter
	body := text
	if m := frontmatterRe.FindStringSubmatch(text); m != nil {
		if err := yaml.Unmarshal([]byte(m[1]), &fm); err != nil {
			// Malformed YAML is not worth discarding the file over: the prose
			// below it is the part that matters.
			fm = Frontmatter{}
		}
		body = text[len(m[0]):]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("the file has no content")
	}

	key := fm.Name
	if key == "" {
		key = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	key = Slug(key)
	if err := store.ValidateKey(key); err != nil {
		return nil, fmt.Errorf("no usable key could be derived from %s", filepath.Base(path))
	}

	memType := strings.ToLower(strings.TrimSpace(fm.Metadata.Type))
	if !validTypes[memType] {
		// An unrecognized type is not a reason to drop a memory. "reference" is
		// the honest default: it is plainly worth keeping, but what kind of
		// claim it makes is unknown.
		memType = "reference"
	}

	description := strings.TrimSpace(fm.Description)
	if description == "" {
		description = firstLine(body)
	}
	if description == "" {
		description = key
	}

	return &Entry{
		SourcePath: path, Key: key, Type: memType,
		Description: description, Body: body,
	}, nil
}

// Slug turns a name into a valid memory key.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 128 {
		s = strings.Trim(s[:128], "-")
	}
	return s
}

// firstLine takes the first meaningful line of prose as a description, skipping
// markdown headings and blank lines.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		return line
	}
	return ""
}

// Import copies a memory directory into a stigmergy scope.
//
// Idempotent by content: re-importing an unchanged file is a no-op, so the same
// directory can be imported repeatedly (after a re-run of init, say) without
// duplicating or churning anything. A file whose content has changed since it
// was imported is reported as a conflict and left alone — the memory may have
// been edited by an agent since, and this has no way to know which version is
// right.
func Import(db *store.DB, dir string, dryRun bool) (*Report, error) {
	files, err := Discover(dir)
	if err != nil {
		return nil, err
	}
	report := &Report{SourceDir: dir, DryRun: dryRun, Outcomes: []Outcome{}}

	for _, path := range files {
		entry, err := Parse(path)
		if err != nil {
			report.Invalid++
			report.Outcomes = append(report.Outcomes, Outcome{
				SourcePath: path, Status: "invalid", Detail: err.Error(),
			})
			continue
		}

		existing, err := db.ReadMemory(entry.Key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// New: import it.
		case err != nil:
			return nil, err
		case existing.Body == entry.Body:
			report.Skipped++
			report.Outcomes = append(report.Outcomes, Outcome{
				SourcePath: path, Key: entry.Key, Status: "skipped",
				Detail: "already imported, unchanged",
			})
			continue
		default:
			report.Conflicts++
			report.Outcomes = append(report.Outcomes, Outcome{
				SourcePath: path, Key: entry.Key, Status: "conflict",
				Detail: fmt.Sprintf("a memory %q already exists (version %d) with different content; "+
					"the source file was left alone and nothing was overwritten", entry.Key, existing.Version),
			})
			continue
		}

		if dryRun {
			report.Imported++
			report.Outcomes = append(report.Outcomes, Outcome{
				SourcePath: path, Key: entry.Key, Status: "imported", Detail: "dry run: nothing was written",
			})
			continue
		}

		_, err = db.WriteMemory(store.MemoryWrite{
			Key: entry.Key, Type: entry.Type, Description: entry.Description,
			Body: entry.Body, UpdatedBy: "importer",
			// Create-only: a CAS conflict here means something appeared between
			// the read above and now, and the right answer is still not to
			// overwrite it.
			ExpectedVersion: nil,
		}, "")
		if err != nil {
			if e, ok := serr.As(err); ok && e.Code == serr.CASConflict {
				report.Conflicts++
				report.Outcomes = append(report.Outcomes, Outcome{
					SourcePath: path, Key: entry.Key, Status: "conflict", Detail: e.Message,
				})
				continue
			}
			return nil, err
		}
		report.Imported++
		report.Outcomes = append(report.Outcomes, Outcome{
			SourcePath: path, Key: entry.Key, Status: "imported",
		})
	}

	return report, nil
}
