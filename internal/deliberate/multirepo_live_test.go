package deliberate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
}

func sh(t *testing.T, s Sandbox, script string) string {
	t.Helper()
	out, err := exec.Command("bwrap", BwrapArgs(s, []string{"/bin/sh", "-c", script})...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoWith(t *testing.T, name, content string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The case multi-repo deliberation exists for: a change that spans a client and
// its service. Before this, only one repository was overlaid — the other sat on
// the read-only base layer, so a worker could READ it and every write failed
// EROFS, part-way through a plan that needed both.
func TestAWorkerCanWriteInEveryMemberRepository(t *testing.T) {
	requireBwrap(t)
	client := repoWith(t, "app.dart", "original client")
	service := repoWith(t, "main.go", "original service")

	s := Sandbox{Repos: []string{client, service}, Chdir: client}
	got := sh(t, s,
		"echo edited > "+client+"/app.dart && echo edited > "+service+"/main.go && "+
			"cat "+client+"/app.dart "+service+"/main.go")
	if got != "edited\nedited" {
		t.Fatalf("could not write in both repositories: %q", got)
	}

	// And neither write is real.
	for _, f := range []struct{ path, want string }{
		{filepath.Join(client, "app.dart"), "original client"},
		{filepath.Join(service, "main.go"), "original service"},
	} {
		body, err := os.ReadFile(f.path)
		if err != nil || strings.TrimSpace(string(body)) != f.want {
			t.Errorf("%s survived as %q (%v), want %q", f.path, body, err, f.want)
		}
	}
}

// Nested repositories both work: an inner checkout inside an outer one is
// unusual but legal, and writes to the inner one must vanish like any other.
//
// Note what this does NOT prove. It was written expecting mount ORDER to matter
// — that an outer overlay mounted second would swallow an inner one — and that
// turns out to be false: mounting over a directory does not unmount what is
// beneath it, and this passes with the sort reversed. The sort is for a stable
// argv, not for correctness. Kept because nesting itself is worth pinning, and
// renamed so it stops claiming to test an ordering rule it cannot see.
func TestNestedRepositoriesAreBothOverlaid(t *testing.T) {
	requireBwrap(t)
	outer, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "vendor", "lib")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "x.go"), []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deliberately handed inner-first, to prove the sort is what fixes it.
	s := Sandbox{Repos: []string{inner, outer}, Chdir: outer}
	got := sh(t, s, "echo changed > "+inner+"/x.go && cat "+inner+"/x.go")
	if got != "changed" {
		t.Fatalf("the nested repository was not writable: %q", got)
	}
	body, _ := os.ReadFile(filepath.Join(inner, "x.go"))
	if strings.TrimSpace(string(body)) != "inner" {
		t.Errorf("a write to the nested repository escaped: %q", body)
	}
}

// THE ordering hazard of this phase.
//
// The run directory moved to XDG state, which lives under $HOME — and $HOME is
// --tmp-overlay'd, not read-only bound. An overlay applied AFTER the mask would
// restore the real directory through its lower layer, and every worker could
// read every other worker's turns: the payload contract defeated silently, with
// nothing in the argv looking wrong.
//
// The mask therefore has to come after the $HOME overlay. This test is the only
// thing that would notice if it stopped.
func TestTheRunDirectoryUnderHomeIsMaskedAfterTheHomeOverlay(t *testing.T) {
	requireBwrap(t)

	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home) // what BwrapArgs overlays

	runRoot := filepath.Join(home, ".local", "state", "stigmergy", "deliberate")
	transcript := filepath.Join(runRoot, "d-test", "slot9-planner.md")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("ANOTHER-AGENTS-TRANSCRIPT"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := repoWith(t, "f.txt", "x")
	s := Sandbox{Repos: []string{repo}, Chdir: repo, RunDirMask: runRoot}

	if got := sh(t, s, "ls -A "+runRoot+" | wc -l"); got != "0" {
		t.Errorf("the run directory was not empty inside the sandbox (%s entries)", got)
	}
	leak := sh(t, s, "{ cat "+transcript+"; } 2>/dev/null; echo done")
	if strings.Contains(leak, "ANOTHER-AGENTS-TRANSCRIPT") {
		t.Fatalf("a worker read another agent's transcript through the $HOME overlay: %q", leak)
	}

	// The driver's real copy is untouched — the mask hides, it does not destroy.
	body, err := os.ReadFile(transcript)
	if err != nil || !strings.Contains(string(body), "ANOTHER-AGENTS-TRANSCRIPT") {
		t.Errorf("the mask destroyed the driver's own artifact: %q (%v)", body, err)
	}
}

// The same assertion at the argv level, so a failure says WHICH ordering rule
// broke rather than only that a worker could read something.
func TestTheRunMaskFollowsTheHomeOverlayInArgv(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	args := BwrapArgs(Sandbox{
		Repos: []string{"/repo"}, Chdir: "/repo", RunDirMask: "/run/mask",
	}, []string{"true"})

	overlay := indexOfPair(args, "--tmp-overlay", home)
	mask := indexOfPair(args, "--tmpfs", "/run/mask")
	if overlay < 0 || mask < 0 {
		t.Fatalf("missing mounts: home overlay=%d mask=%d in %v", overlay, mask, args)
	}
	if mask < overlay {
		t.Errorf("the run mask (%d) precedes the $HOME overlay (%d); the overlay would restore it", mask, overlay)
	}
}

// Every member repository's legacy in-tree run directory is masked too. An old
// run left in .git/deliberate is still somebody else's transcript.
func TestEveryMemberGetsItsLegacyRunDirectoryMasked(t *testing.T) {
	args := BwrapArgs(Sandbox{Repos: []string{"/a", "/b"}, Chdir: "/a"}, []string{"true"})
	for _, want := range []string{"/a/.git/deliberate", "/b/.git/deliberate"} {
		if indexOfPair(args, "--tmpfs", want) < 0 {
			t.Errorf("%s was not masked: %v", want, args)
		}
	}
}

// Hosts whose directory flag is repeatable must be told about every member.
//
// bwrap overlays them all regardless, but a host's own flag decides what its
// tools will touch: claude's --add-dir gates tool access, and agy ignores cwd
// entirely. Told about one repository, a worker is blocked by its own host from
// editing a sibling the sandbox is perfectly happy to let it write — which looks
// like the sandbox failing and is not.
//
// This existed as an unused Agent.WorkDirs() for a while: the helper was written
// and never wired in, so the argv still named one directory. Caught by a
// dead-code sweep, not by a test, hence this one.
func TestRepeatableDirectoryFlagsNameEveryMember(t *testing.T) {
	a := &Agent{
		Kind: "claude-code", Model: "m", Workdir: "/client",
		Confine: Sandbox{Repos: []string{"/client", "/service"}},
	}
	got := a.WorkDirs()
	if len(got) != 2 || got[0] != "/client" || got[1] != "/service" {
		t.Fatalf("WorkDirs() = %v, want both members", got)
	}

	// And with no project configured it degrades to the single workdir, which is
	// what every single-repository run does.
	solo := &Agent{Kind: "claude-code", Workdir: "/only"}
	if w := solo.WorkDirs(); len(w) != 1 || w[0] != "/only" {
		t.Errorf("WorkDirs() with no members = %v, want [/only]", w)
	}
}
