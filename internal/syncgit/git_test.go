package syncgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunnerUsesItsOwnIdentityAndDisablesHooks exercises a real bare remote.
// A commit hook that exits non-zero proves every committing invocation carries
// core.hooksPath=/dev/null; the inspected commit proves the user identity is
// never inherited from the host configuration.
func TestRunnerUsesItsOwnIdentityAndDisablesHooks(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "remote.git")
	git(t, "init", "--bare", bare)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := (Runner{}).Clone(bare, clone); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(clone, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 17\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "memory.md"), []byte("memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Runner{Dir: clone}
	if err := r.AddAll(); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("sync"); err != nil {
		t.Fatalf("commit ran a user hook or missed identity: %v", err)
	}
	who, err := r.Output("log", "-1", "--format=%an <%ae>")
	if err != nil {
		t.Fatal(err)
	}
	if who != "stigmergy <stigmergy@localhost>" {
		t.Fatalf("commit identity = %q", who)
	}
	if err := r.Push(); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
}
