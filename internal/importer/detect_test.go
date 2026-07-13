package importer

import "testing"

// The slug must match what Claude Code actually produces, or the detector looks
// in a directory that does not exist and the user's memories are silently
// stranded. This is the real slug for this very repository — note that the
// space in "Active Code" is flattened, not just the path separators.
func TestProjectSlugMatchesClaudeCodesNaming(t *testing.T) {
	cases := map[string]string{
		"/mnt/sdb1/Code/Active Code/multi-agentic-memories": "-mnt-sdb1-Code-Active-Code-multi-agentic-memories",
		"/home/user/repo":     "-home-user-repo",
		"/home/user/my.thing": "-home-user-my-thing",
	}
	for in, want := range cases {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
