package paths

import "testing"

var members = []string{"naviamp", "naviamp-sidecar"}

func TestParseScope(t *testing.T) {
	for _, tc := range []struct {
		name             string
		spec, self       string
		wantRepo, wantSc string
		wantErr          bool
	}{
		{name: "bare path means the repository you opened",
			spec: "src/main.go", self: "naviamp", wantRepo: "naviamp", wantSc: "src/main.go"},
		{name: "explicit repository",
			spec: "naviamp-sidecar:internal/api.go", self: "naviamp",
			wantRepo: "naviamp-sidecar", wantSc: "internal/api.go"},
		{name: "the repository root",
			spec: "naviamp-sidecar:.", self: "naviamp", wantRepo: "naviamp-sidecar", wantSc: "."},
		{name: "single-repo projects have no self and need no prefix",
			spec: "src/main.go", self: "", wantRepo: "", wantSc: "src/main.go"},

		// The ambiguity, decided: a colon whose head is not a member is part of
		// the path. ':' is a legal filename character and always has been.
		{name: "a filename containing a colon is a filename",
			spec: "weird:name.go", self: "naviamp", wantRepo: "naviamp", wantSc: "weird:name.go"},
		{name: "only the FIRST colon splits",
			spec: "naviamp:odd:file.go", self: "naviamp", wantRepo: "naviamp", wantSc: "odd:file.go"},
		{name: "a colon in a deeper component is untouched",
			spec: "src/a:b.go", self: "naviamp", wantRepo: "naviamp", wantSc: "src/a:b.go"},

		{name: "a repository with no path is refused",
			spec: "naviamp:", self: "naviamp", wantErr: true},
		{name: "empty is refused", spec: "", self: "naviamp", wantErr: true},
		{name: "absolute is refused", spec: "/etc/passwd", self: "naviamp", wantErr: true},
		{name: "escaping the repository is refused",
			spec: "../outside", self: "naviamp", wantErr: true},
		{name: "escaping under a named repository is refused",
			spec: "naviamp-sidecar:../../etc", self: "naviamp", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, scope, err := ParseScope(tc.spec, members, tc.self)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseScope(%q) = (%q, %q), want an error", tc.spec, repo, scope)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseScope(%q): %v", tc.spec, err)
			}
			if repo != tc.wantRepo || scope != tc.wantSc {
				t.Errorf("ParseScope(%q) = (%q, %q), want (%q, %q)",
					tc.spec, repo, scope, tc.wantRepo, tc.wantSc)
			}
		})
	}
}

// The failure this guards against is silent and plausible: "sidecar:src/x.go"
// when the member is called "naviamp-sidecar" parses as a PATH, the claim
// succeeds, and it guards a file that does not exist while the real one stays
// unprotected. Nothing errors on its own.
func TestUnknownRepoHint(t *testing.T) {
	if got := UnknownRepoHint("sidecar:src/x.go", members); got == "" {
		t.Error("a near-miss repository prefix produced no hint")
	}
	if got := UnknownRepoHint("naviamp:src/x.go", members); got != "" {
		t.Errorf("a real repository produced a hint: %q", got)
	}
	if got := UnknownRepoHint("src/x.go", members); got != "" {
		t.Errorf("a path with no colon produced a hint: %q", got)
	}
	// A single-repo project has no roster to be wrong about, and "weird:name.go"
	// there is simply a filename.
	if got := UnknownRepoHint("weird:name.go", nil); got != "" {
		t.Errorf("a single-repo project produced a hint: %q", got)
	}
}
