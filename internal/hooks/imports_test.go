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
