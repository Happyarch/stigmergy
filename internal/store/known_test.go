package store

import (
	"errors"
	"testing"
	"time"
)

// testGlobal and testProject live in memories_test.go.

func TestRememberProjectRoundTrips(t *testing.T) {
	g := testGlobal(t)

	if err := g.RememberProject("/repo/.git/stigmergy.sqlite3", "/repo"); err != nil {
		t.Fatalf("RememberProject: %v", err)
	}
	got, err := g.KnownProjects()
	if err != nil {
		t.Fatalf("KnownProjects: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1", len(got))
	}
	if got[0].DBPath != "/repo/.git/stigmergy.sqlite3" || got[0].Label != "/repo" {
		t.Fatalf("unexpected row: %+v", got[0])
	}
	if got[0].FirstSeen == "" || got[0].LastSeen == "" {
		t.Fatalf("timestamps not populated: %+v", got[0])
	}
}

// Re-registering the same project must not duplicate it, because every doctor
// run and every context_open calls this. It must also not overwrite first_seen:
// the column would otherwise track "last opened" under a name that says
// otherwise, and the registry's only historical claim would be silently false.
func TestRememberProjectIsIdempotentAndKeepsFirstSeen(t *testing.T) {
	g := testGlobal(t)

	restore := SetClock(func() time.Time { return time.Unix(1000, 0).UTC() })
	if err := g.RememberProject("/repo/.git/stigmergy.sqlite3", "/repo"); err != nil {
		t.Fatalf("first RememberProject: %v", err)
	}
	restore()

	first, _ := g.KnownProjects()

	restore = SetClock(func() time.Time { return time.Unix(9000, 0).UTC() })
	if err := g.RememberProject("/repo/.git/stigmergy.sqlite3", "/repo-renamed"); err != nil {
		t.Fatalf("second RememberProject: %v", err)
	}
	restore()

	got, err := g.KnownProjects()
	if err != nil {
		t.Fatalf("KnownProjects: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1 after re-registering the same path", len(got))
	}
	if got[0].FirstSeen != first[0].FirstSeen {
		t.Errorf("first_seen changed: was %q, now %q", first[0].FirstSeen, got[0].FirstSeen)
	}
	if got[0].LastSeen == first[0].LastSeen {
		t.Errorf("last_seen did not advance: still %q", got[0].LastSeen)
	}
	// The label is allowed to move — a repository can be renamed on disk, and
	// the newer answer is the more useful one to print.
	if got[0].Label != "/repo-renamed" {
		t.Errorf("label = %q, want the refreshed one", got[0].Label)
	}
}

func TestForgetProject(t *testing.T) {
	g := testGlobal(t)
	if err := g.RememberProject("/a/.git/stigmergy.sqlite3", "/a"); err != nil {
		t.Fatal(err)
	}
	if err := g.RememberProject("/b/.git/stigmergy.sqlite3", "/b"); err != nil {
		t.Fatal(err)
	}
	if err := g.ForgetProject("/a/.git/stigmergy.sqlite3"); err != nil {
		t.Fatalf("ForgetProject: %v", err)
	}
	got, _ := g.KnownProjects()
	if len(got) != 1 || got[0].Label != "/b" {
		t.Fatalf("after forgetting /a, got %+v", got)
	}
}

// The registry spans projects, so it lives in the global database. Asking a
// project database for it is a category error, and it must fail as one rather
// than as a confusing "no such table" from somewhere deeper.
func TestRegistryRejectsAProjectDatabase(t *testing.T) {
	p := testProject(t)

	if err := p.RememberProject("/x/.git/stigmergy.sqlite3", "/x"); !errors.Is(err, ErrNotGlobal) {
		t.Errorf("RememberProject on a project DB = %v, want ErrNotGlobal", err)
	}
	if _, err := p.KnownProjects(); !errors.Is(err, ErrNotGlobal) {
		t.Errorf("KnownProjects on a project DB = %v, want ErrNotGlobal", err)
	}
	if err := p.ForgetProject("/x"); !errors.Is(err, ErrNotGlobal) {
		t.Errorf("ForgetProject on a project DB = %v, want ErrNotGlobal", err)
	}
}

func TestRememberProjectRejectsAnEmptyPath(t *testing.T) {
	g := testGlobal(t)
	if err := g.RememberProject("", "/repo"); err == nil {
		t.Fatal("an empty database path was accepted")
	}
}
