package hooks

import (
	"os/exec"
	"strings"
	"testing"
)

// The hook path runs as a fresh process on every single Edit and Write, inside a
// latency budget measured in milliseconds. internal/drift spawns git
// subprocesses — several of them, per repository — so it must never become
// reachable from here, however indirectly.
//
// Asserted on the transitive import graph rather than on this package's own
// import block, because the way this breaks is a helper three packages down
// picking up a dependency nobody looked at.
func TestHooksDoNotReachTheDriftPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/drift") {
			t.Fatalf("internal/hooks now depends on %s — git subprocesses on the edit path", dep)
		}
	}
}

// The same ban, for the same reason, on sync's transport.
//
// It is deliberately NOT a ban on internal/syncx. That package is plain data
// and a pure merger, and internal/store returns its types — so hooks reach it
// transitively the moment they open a database, and banning it would be banning
// a struct. The hazard was never the struct. internal/syncgit spawns git and
// talks to a network, either of which can block indefinitely on a credential
// prompt inside an agent's turn, and that is what must stay unreachable from a
// path with a 250ms lock budget (docs/sync-model.md §6.2, D14).
func TestHooksDoNotReachTheSyncTransport(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/syncgit") {
			t.Fatalf("internal/hooks now depends on %s — git subprocesses and network I/O on the edit path", dep)
		}
	}
}
