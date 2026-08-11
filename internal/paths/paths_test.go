package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	// The temp dir itself may be a symlink (/tmp → /private/tmp and friends),
	// so resolve it up front: the worktree being compared against must already be
	// in resolved form or every path looks like an escape.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(root, "src", "api"))
	mkdir(t, filepath.Join(root, "docs"))
	touch(t, filepath.Join(root, "src", "main.go"))
	return root
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize(t *testing.T) {
	root := setup(t)

	cases := []struct {
		name, cwd, path, want string
	}{
		{"absolute path", root, filepath.Join(root, "src", "main.go"), "src/main.go"},
		{"relative to cwd", root, "src/main.go", "src/main.go"},
		{"relative to a subdir cwd", filepath.Join(root, "src"), "main.go", "src/main.go"},
		{"dot segments collapse", root, "src/../src/./main.go", "src/main.go"},
		{"file that does not exist yet", root, "src/api/new.go", "src/api/new.go"},
		{"deep path that does not exist yet", root, "a/b/c/new.go", "a/b/c/new.go"},
		{"the worktree root itself", root, root, "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(root, c.cwd, c.path)
			if err != nil {
				t.Fatalf("Normalize(%q) errored: %v", c.path, err)
			}
			if got != c.want {
				t.Fatalf("Normalize(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// A symlink is a second spelling of the same file. If it normalized to a
// different path, a claim on the real file would not protect edits made through
// the link.
func TestNormalizeResolvesSymlinks(t *testing.T) {
	root := setup(t)
	if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := Normalize(root, root, "link/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "src/main.go" {
		t.Fatalf("path through a symlink normalized to %q, want src/main.go — a claim on the real file would not cover it", got)
	}

	// The same holds for a file that does not exist yet inside a linked dir.
	got, err = Normalize(root, root, "link/api/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "src/api/new.go" {
		t.Fatalf("new file through a symlink normalized to %q, want src/api/new.go", got)
	}
}

func TestNormalizeDetectsEscapes(t *testing.T) {
	root := setup(t)
	outside := t.TempDir()
	touch(t, filepath.Join(outside, "elsewhere.go"))

	for _, p := range []string{
		filepath.Join(outside, "elsewhere.go"),
		"../elsewhere.go",
		filepath.Join(root, "..", filepath.Base(outside), "elsewhere.go"),
	} {
		if _, err := Normalize(root, root, p); !errors.Is(err, ErrOutsideWorktree) {
			t.Errorf("Normalize(%q) = %v, want ErrOutsideWorktree", p, err)
		}
	}

	// A symlink pointing out of the tree is an escape too, however innocent it
	// looks from inside.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Normalize(root, root, "escape/elsewhere.go"); !errors.Is(err, ErrOutsideWorktree) {
		t.Errorf("a symlink out of the worktree must be detected as an escape, got %v", err)
	}
}

func TestValidateScope(t *testing.T) {
	ok := map[string]string{
		"src":            "src",
		"src/api":        "src/api",
		"./src/":         "src",
		"src/../src":     "src",
		".":              ".",
		"src/main.go":    "src/main.go",
		"a/b/../c/d.txt": "a/c/d.txt",
	}
	for in, want := range ok {
		got, err := ValidateScope(in)
		if err != nil {
			t.Errorf("ValidateScope(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateScope(%q) = %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"", "/abs/path", "..", "../outside", "src/../.."} {
		if _, err := ValidateScope(bad); err == nil {
			t.Errorf("ValidateScope(%q) must be rejected", bad)
		}
	}
}
