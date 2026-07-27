package deliberate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the real sandbox with /bin/sh standing in for a host.
//
// sandbox_test.go inspects the argv, which catches ordering mistakes but cannot
// catch a mount that is ordered correctly and still does not do what the comment
// says. Nothing here needs a model, an API key, or a host CLI — the isolation
// properties the payload contract rests on are testable for free, and were not
// being tested at all.
func liveSandbox(t *testing.T) (repo string, run func(payload, script string) string) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A run directory with another slot's transcript already in it.
	runDir := filepath.Join(repo, ".git", "deliberate", "d-test")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "slot9-planner.md"),
		[]byte("ANOTHER-AGENTS-TRANSCRIPT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	return repo, func(payload, script string) string {
		t.Helper()
		path := ""
		if payload != "" {
			path = filepath.Join(t.TempDir(), "brief.md")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		args := BwrapArgs(Sandbox{Repos: []string{repo}, Chdir: repo, StateDirs: nil, PayloadPath: path}, []string{"/bin/sh", "-c", script})
		out, err := exec.Command("bwrap", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("bwrap: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
}

// The worker can read its own brief, at the fixed path it is told about.
func TestAWorkerCanReadItsBrief(t *testing.T) {
	_, run := liveSandbox(t)
	got := run("THE-BRIEF-FOR-THIS-TURN", "cat "+SandboxPayloadPath)
	if got != "THE-BRIEF-FOR-THIS-TURN" {
		t.Errorf("brief read back as %q", got)
	}
}

// The brief is read-only. A worker that rewrote its own instructions and then
// re-read them would be a confusing failure to diagnose.
//
// The redirection is grouped so its own failure message is swallowed: a shell
// applies redirections left to right, so `> file 2>/dev/null` reports the failed
// open before 2>/dev/null takes effect.
func TestTheBriefIsReadOnly(t *testing.T) {
	_, run := liveSandbox(t)
	got := run("original brief",
		"{ echo tampered > "+SandboxPayloadPath+"; } 2>/dev/null; cat "+SandboxPayloadPath)
	if got != "original brief" {
		t.Errorf("the brief was writable: %q", got)
	}
}

// The repository is an overlay: the worker sees the real working tree, may write
// anywhere in it, and none of it survives. This is what makes it safe to point
// an auto-approving agent at the real path.
func TestWritesToTheRepositoryVanish(t *testing.T) {
	repo, run := liveSandbox(t)

	got := run("", "echo changed > "+repo+"/tracked.txt && echo new > "+repo+"/created.txt && cat "+repo+"/tracked.txt")
	if got != "changed" {
		t.Errorf("the worker could not write in the repository: %q", got)
	}

	// On the host, nothing happened.
	body, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "original" {
		t.Errorf("a worker's write reached the real repository: %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "created.txt")); err == nil {
		t.Error("a file the worker created survived into the real repository")
	}
}

// The run directory is masked, so one worker cannot read another's turns.
//
// This is the payload contract enforced by the kernel rather than by the
// prompts: the Adversary must not be able to go and read the Guide's questions,
// and a Planner must not be able to read the critique history the design
// withholds from it. Nothing in the prompts invites that — the point is that it
// must not be POSSIBLE.
func TestAWorkerCannotReadTheRunDirectory(t *testing.T) {
	repo, run := liveSandbox(t)

	// The tmpfs covers the whole deliberate directory, not one run inside it, so
	// a worker sees an empty directory and the run it belongs to does not appear
	// to exist at all.
	got := run("", "ls -A "+repo+"/.git/deliberate | wc -l")
	if got != "0" {
		t.Errorf("the deliberate directory was not empty inside the sandbox (%s entries)", got)
	}

	leak := run("", "{ cat "+repo+"/.git/deliberate/d-test/slot9-planner.md; } 2>/dev/null; echo done")
	if strings.Contains(leak, "ANOTHER-AGENTS-TRANSCRIPT") {
		t.Errorf("a worker read another agent's transcript: %q", leak)
	}

	// And the driver's real copy is untouched by the mask.
	body, err := os.ReadFile(filepath.Join(repo, ".git", "deliberate", "d-test", "slot9-planner.md"))
	if err != nil || !strings.Contains(string(body), "ANOTHER-AGENTS-TRANSCRIPT") {
		t.Errorf("the mask destroyed the driver's own artifact: %q (%v)", body, err)
	}
}

// A large brief goes through untouched. The whole reason it left argv.
func TestALargeBriefSurvivesTheBoundary(t *testing.T) {
	_, run := liveSandbox(t)
	big := strings.Repeat("abcdefgh", 40_000) // 320 KB, well past MAX_ARG_STRLEN
	got := run(big, "wc -c < "+SandboxPayloadPath)
	if got != "320000" {
		t.Errorf("brief arrived as %s bytes, want 320000", got)
	}
}

// Everything outside the declared repositories, $HOME and the state dirs is
// READ-ONLY, and that is what the rest of the confinement rests on.
//
// The overlays make specific places writable-but-ephemeral. They say nothing
// about anywhere else — the property that a worker cannot scribble on the rest
// of the machine comes from one line, `--ro-bind / /`, laid down before them as
// the base. Nothing asserted it: remove that line and every other sandbox test
// still passes, while a worker gains write access to the whole filesystem.
//
// /etc is used because it is guaranteed to exist, is outside every overlay, and
// is nowhere near anything this project owns. The probe path is checked on the
// host afterwards and the test fails loudly if it ever appears.
func TestTheFilesystemOutsideTheOverlaysIsReadOnly(t *testing.T) {
	_, run := liveSandbox(t)
	const probe = "/etc/stigmergy-sandbox-probe"

	out := run("", "touch "+probe+" 2>&1; echo EXIT=$?")
	if !strings.Contains(out, "EXIT=1") {
		t.Errorf("touching %s inside the sandbox did not fail: %q", probe, out)
	}
	if !strings.Contains(strings.ToLower(out), "read-only") {
		t.Errorf("the failure was not a read-only filesystem: %q", out)
	}
	if _, err := os.Stat(probe); err == nil {
		os.Remove(probe)
		t.Fatalf("a worker created %s on the real filesystem — the read-only root is gone", probe)
	}
}

// Reading outside is allowed, and that is the intended trade: a worker needs its
// toolchain, its libraries and its interpreters, all of which live out there.
// Confinement here is about what a worker can CHANGE, not what it can see.
func TestReadingOutsideTheOverlaysStillWorks(t *testing.T) {
	_, run := liveSandbox(t)
	if got := run("", "test -r /etc/hostname && echo readable"); got != "readable" {
		t.Errorf("a worker could not read /etc: %q — its tools would stop working", got)
	}
}

// The worker gets a FRESH /tmp, so nothing the host left there is visible.
//
// Found by writing the test above wrongly: the first version put its fixture in
// t.TempDir(), which is under /tmp, and the file simply was not there inside the
// sandbox. That is a stronger property than the one being tested and it was not
// asserted anywhere — worth pinning, because the payload is bind-mounted at
// /tmp/stigmergy-payload.md and therefore depends on this tmpfs being laid down
// FIRST. Reorder those two and the brief disappears.
func TestTheWorkerGetsAFreshTmp(t *testing.T) {
	_, run := liveSandbox(t)

	hostFile := filepath.Join(os.TempDir(), "stigmergy-host-tmp-probe.txt")
	if err := os.WriteFile(hostFile, []byte("HOST"), 0o600); err != nil {
		t.Skipf("cannot write to the host /tmp: %v", err)
	}
	defer os.Remove(hostFile)

	if got := run("", "cat "+hostFile+" 2>&1 || true"); strings.Contains(got, "HOST") {
		t.Errorf("the host's /tmp is visible to the worker: %q", got)
	}
	// And the payload still arrives, which is the thing that depends on the
	// tmpfs going down before the bind.
	if got := run("the brief", "cat "+SandboxPayloadPath); got != "the brief" {
		t.Errorf("the brief did not survive the /tmp tmpfs: %q", got)
	}
}
