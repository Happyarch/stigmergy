package claims

import "testing"

func TestOverlaps(t *testing.T) {
	file := func(p string) Scope { return Scope{Path: p} }
	dir := func(p string) Scope { return Scope{Path: p, Recursive: true} }

	cases := []struct {
		name string
		a, b Scope
		want bool
	}{
		{"same file", file("src/main.go"), file("src/main.go"), true},
		{"different files", file("src/main.go"), file("src/other.go"), false},

		{"dir covers file inside", dir("src"), file("src/main.go"), true},
		{"dir covers file deep inside", dir("src"), file("src/a/b/c.go"), true},
		{"dir covers itself as a file", dir("src"), file("src"), true},
		{"dir does not cover a sibling", dir("src"), file("docs/main.go"), false},
		{"file inside dir, reversed", file("src/main.go"), dir("src"), true},

		{"nested dirs overlap", dir("src"), dir("src/api"), true},
		{"nested dirs overlap, reversed", dir("src/api"), dir("src"), true},
		{"sibling dirs do not overlap", dir("src/api"), dir("src/web"), false},
		{"same dir", dir("src"), dir("src"), true},

		// The prefix trap: a raw strings.HasPrefix would call every one of
		// these an overlap, silently blocking edits to unrelated files.
		{"foo does not cover foobar", dir("foo"), file("foobar"), false},
		{"foo does not cover foobar/x", dir("foo"), file("foobar/x.go"), false},
		{"foo dir vs foobar dir", dir("foo"), dir("foobar"), false},
		{"src/api does not cover src/apiv2", dir("src/api"), dir("src/apiv2"), false},
		{"file foo vs file foobar", file("foo"), file("foobar"), false},

		// The repo root is spelled "." while everything under it is spelled
		// without a leading "./", so it needs its own handling.
		{"repo root covers itself", dir("."), file("."), true},
		{"repo root covers a top-level file", dir("."), file("main.go"), true},
		{"repo root covers a deep file", dir("."), file("src/api/handlers.go"), true},
		{"repo root covers any subtree", dir("."), dir("src"), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Overlaps(c.a, c.b); got != c.want {
				t.Errorf("Overlaps(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
			// Overlap is symmetric by definition; assert it, because an
			// asymmetric bug here would make enforcement depend on argument
			// order, which is exactly the kind of thing that hides for months.
			if got := Overlaps(c.b, c.a); got != c.want {
				t.Errorf("Overlaps(%v, %v) = %v, want %v (not symmetric)", c.b, c.a, got, c.want)
			}
		})
	}
}

func TestCovers(t *testing.T) {
	if !Covers(Scope{Path: "src", Recursive: true}, "src/deep/file.go") {
		t.Error("a recursive claim on src must cover src/deep/file.go")
	}
	if Covers(Scope{Path: "src", Recursive: false}, "src/deep/file.go") {
		t.Error("a file claim on src must not cover files under it")
	}
}
