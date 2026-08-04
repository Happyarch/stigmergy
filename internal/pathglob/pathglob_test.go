package pathglob

import "testing"

// Same cases internal/drift's TestPathspecs exercises against real git, here
// as a pure unit test of the Go-side matcher.
func TestMatchGlobDoesNotCrossDirectorySeparators(t *testing.T) {
	if !Match("internal/*", "internal/top.go") {
		t.Error("internal/* should match internal/top.go")
	}
	if Match("internal/*", "internal/sub/deep.go") {
		t.Error("internal/* crossed a directory separator into internal/sub/deep.go")
	}
	if !Match("internal/**", "internal/top.go") {
		t.Error("internal/** should match internal/top.go")
	}
	if !Match("internal/**", "internal/sub/deep.go") {
		t.Error("internal/** should match internal/sub/deep.go")
	}
}

func TestMatchGlobBraceIsLiteral(t *testing.T) {
	// git's :(glob) has no brace expansion either — internal/drift's
	// adversarial test pins the same expectation against real git.
	if Match("*.{go,md}", "file.go") {
		t.Error("brace patterns must not be expanded; {go,md} is literal text")
	}
	if !Match("*.{go,md}", "file.{go,md}") {
		t.Error("the literal braces themselves should still match")
	}
}

func TestMatchGlobBasics(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"docs/*.md", "docs/readme.md", true},
		{"docs/*.md", "docs/sub/readme.md", false},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"**", "anything/at/all.txt", true},
		{"internal/store/*.go", "internal/store/links.go", true},
		{"internal/store/*.go", "internal/store/links_test.go", true},
		{"internal/store/*.go", "internal/hooks/priming.go", false},
		{"exact.go", "exact.go", true},
		{"exact.go", "other.go", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestStaticPrefix(t *testing.T) {
	cases := []struct{ pattern, want string }{
		{"internal/**", "internal"},
		{"internal/store/*.go", "internal/store"},
		{"*.go", "."},
		{"src/*/handlers.go", "src"},
		{"internal/store/links.go", "internal/store/links.go"},
	}
	for _, c := range cases {
		if got := StaticPrefix(c.pattern); got != c.want {
			t.Errorf("StaticPrefix(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}
