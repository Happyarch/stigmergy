package cli

import "testing"

// --out may land in ANY repository of the run, not just the one you invoked
// from. A spec spanning a client and its service may well belong in the
// service's docs, and requiring it to land where you happened to be standing
// would restrict the exact case multi-repo deliberation exists for.
func TestOutMayLandInAnyMemberRepository(t *testing.T) {
	repos := []string{"/code/client", "/code/service"}

	for _, ok := range []string{
		"/code/client/docs/spec.md",
		"/code/service/docs/spec.md",
		"/code/service/spec.md",
	} {
		if err := insideRepos(repos, ok); err != nil {
			t.Errorf("insideRepos(%q) = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{
		"/etc/passwd",
		"/code/client/../../../tmp/x.md",
		"/code/other/spec.md",
		"/code/client-old/spec.md", // shares a prefix, is not a member
	} {
		if err := insideRepos(repos, bad); err == nil {
			t.Errorf("insideRepos(%q) was accepted; it escapes every member", bad)
		}
	}
}
