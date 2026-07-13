package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/happyarch/stigmergy/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func memoryDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const withFrontmatter = `---
name: build-system
description: How this project is built
metadata:
  type: project
---

The build uses ` + "`go build`" + ` with CGO disabled.
`

const withoutFrontmatter = `# Deploy process

Deploys run from CI on a tag push.
`

func TestImportParsesBothShapes(t *testing.T) {
	db := testDB(t)
	dir := memoryDir(t, map[string]string{
		"build-system.md":    withFrontmatter,
		"deploy-process.md":  withoutFrontmatter,
		"MEMORY.md":          "# Index\n\n- [Build](build-system.md)\n",
		"nested/api-conv.md": "API handlers return errors, never panic.\n",
	})

	report, err := Import(db, dir, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Imported != 3 || report.Invalid != 0 {
		t.Fatalf("report = %+v, want 3 imported (the index is not a memory)", report)
	}

	// Frontmatter supplies key, type and description.
	m, err := db.ReadMemory("build-system")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "project" || m.Description != "How this project is built" {
		t.Fatalf("frontmatter was not honored: %+v", m)
	}
	if m.UpdatedBy != "importer" {
		t.Fatalf("updated_by = %q, want importer — imported memories must be traceable", m.UpdatedBy)
	}

	// Without frontmatter: key from the filename, description from the prose,
	// and a conservative type.
	m, err = db.ReadMemory("deploy-process")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "Deploy process" {
		t.Fatalf("description = %q, want the first line of prose", m.Description)
	}
	if m.Type != "reference" {
		t.Fatalf("type = %q, want the reference default", m.Type)
	}

	if _, err := db.ReadMemory("api-conv"); err != nil {
		t.Fatalf("a nested memory file was not imported: %v", err)
	}
	if _, err := db.ReadMemory("memory"); err == nil {
		t.Fatal("the index file was imported as a memory")
	}

	// The imported memories must be searchable, or the import was pointless.
	hits, err := db.SearchMemories("CGO disabled")
	if err != nil || len(hits) != 1 {
		t.Fatalf("imported memories are not searchable: %d hits, err=%v", len(hits), err)
	}
}

// Re-importing is routine — init may run again, or a user may re-run it. It
// must be a no-op, not a source of churn or duplicates.
func TestImportIsIdempotent(t *testing.T) {
	db := testDB(t)
	dir := memoryDir(t, map[string]string{"build-system.md": withFrontmatter})

	first, err := Import(db, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 1 {
		t.Fatalf("first import = %+v", first)
	}

	second, err := Import(db, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Skipped != 1 || second.Conflicts != 0 {
		t.Fatalf("re-import = %+v, want 1 skipped and nothing else", second)
	}

	m, err := db.ReadMemory("build-system")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 {
		t.Fatalf("version = %d, want 1 — a re-import must not churn the memory", m.Version)
	}
}

// The case that matters most: an agent has since edited the memory, and the
// file on disk still holds the old text. Overwriting either one silently
// destroys someone's work, so the import refuses and says so.
func TestImportNeverOverwritesADivergedMemory(t *testing.T) {
	db := testDB(t)
	dir := memoryDir(t, map[string]string{"build-system.md": withFrontmatter})

	if _, err := Import(db, dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteMemory(store.MemoryWrite{
		Key: "build-system", Type: "project", Description: "How this project is built",
		Body: "The build now uses bazel.", UpdatedBy: "r-agent", ExpectedVersion: intp(1),
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}

	report, err := Import(db, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 1 || report.Imported != 0 {
		t.Fatalf("report = %+v, want 1 conflict and nothing imported", report)
	}

	// The agent's version survives untouched.
	m, err := db.ReadMemory("build-system")
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "The build now uses bazel." || m.Version != 2 {
		t.Fatalf("the import clobbered the agent's memory: %+v", m)
	}

	// And so does the file.
	data, err := os.ReadFile(filepath.Join(dir, "build-system.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != withFrontmatter {
		t.Fatal("the import modified the source file")
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	db := testDB(t)
	dir := memoryDir(t, map[string]string{"build-system.md": withFrontmatter})

	report, err := Import(db, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 1 || !report.DryRun {
		t.Fatalf("dry run report = %+v", report)
	}
	if _, err := db.ReadMemory("build-system"); err == nil {
		t.Fatal("a dry run wrote to the database")
	}
}

func TestImportHandlesBadFiles(t *testing.T) {
	db := testDB(t)
	dir := memoryDir(t, map[string]string{
		"empty.md":            "",
		"only-frontmatter.md": "---\nname: x\n---\n",
		"bad-yaml.md":         "---\nname: [unclosed\n---\n\nBut the prose is fine.\n",
		"UPPER Case Name.md":  "Content with an awkward filename.\n",
		"good.md":             "A perfectly ordinary memory.\n",
	})

	report, err := Import(db, dir, false)
	if err != nil {
		t.Fatalf("one bad file must not abort the import: %v", err)
	}
	if report.Invalid != 2 {
		t.Fatalf("invalid = %d, want 2 (the empty file and the one with no body)", report.Invalid)
	}
	// Malformed YAML must not cost us the prose underneath it.
	if _, err := db.ReadMemory("bad-yaml"); err != nil {
		t.Errorf("a file with broken frontmatter but real content was dropped: %v", err)
	}
	// An awkward filename is slugified rather than rejected.
	if _, err := db.ReadMemory("upper-case-name"); err != nil {
		t.Errorf("a file with an awkward name was not slugified into a valid key: %v", err)
	}
	if _, err := db.ReadMemory("good"); err != nil {
		t.Errorf("a good file was not imported alongside the bad ones: %v", err)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"build-system":        "build-system",
		"Build System":        "build-system",
		"user_language_prefs": "user-language-prefs",
		"  spaced  out  ":     "spaced-out",
		"--leading--":         "leading",
		"CamelCase":           "camelcase",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func intp(i int) *int { return &i }
