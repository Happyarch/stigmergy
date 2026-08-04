package hooks

import (
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/store"
)

// primedFixture wires a claim, an evidence policy that covers it, and a link
// on the primed memory — the fixture docs/association-model.md §12 asks for.
func primedFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	root := f.claim(t, "sess-worker", "src/main.go", false)

	if err := f.db.AddRepo("app", f.worktree+"/.git", f.worktree); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if _, err := f.db.WriteMemory(store.MemoryWrite{
		Key: "wire-format", Type: "project", Description: "The wire format is versioned.",
		Body: "See src/main.go for the framing code.", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("write primed memory: %v", err)
	}
	if _, err := f.db.WriteMemory(store.MemoryWrite{
		Key: "framing-history", Type: "project", Description: "Why framing changed in v2.",
		Body: "Body.", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatalf("write neighbor memory: %v", err)
	}
	if _, err := f.db.CreateLink("framing-history", "wire-format", "explains the versioning decision", "r-test", "claude-code"); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if _, err := f.db.SetEvidencePolicy(store.EvidenceSet{
		Key: "wire-format", ExpectedMemoryVersion: 1,
		Members: []store.EvidenceMember{{RepoID: "app", BaseOID: "deadbeef"}},
		Actor:   "r-test", AgentKind: "claude-code",
	}); err != nil {
		t.Fatalf("SetEvidencePolicy: %v", err)
	}
	return f, root
}

// A session whose claim overlaps a memory's (whole-repo, zero-path) evidence
// policy is primed on that memory, and the note includes its linked neighbor
// with the edge's reason.
func TestPrimingFindsClaimedFilesEvidenceAndNeighbor(t *testing.T) {
	f, _ := primedFixture(t)

	p := CheckPriming("claude-code", "sess-worker", f.worktree)
	if len(p.Primed) != 1 || p.Primed[0].Key != "wire-format" {
		t.Fatalf("Primed = %+v, want [wire-format]", p.Primed)
	}
	if p.Primed[0].Description != "The wire format is versioned." {
		t.Errorf("primed description = %q", p.Primed[0].Description)
	}
	if len(p.Primed[0].Neighbors) != 1 || p.Primed[0].Neighbors[0].Key != "framing-history" {
		t.Fatalf("neighbors = %+v, want [framing-history]", p.Primed[0].Neighbors)
	}
	if p.Primed[0].Neighbors[0].Reason != "explains the versioning decision" {
		t.Errorf("neighbor reason = %q", p.Primed[0].Neighbors[0].Reason)
	}

	text := PrimingText(p)
	for _, want := range []string{
		"wire-format", "The wire format is versioned.",
		"framing-history", "explains the versioning decision",
		"memory_verify", "update or unlink",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("priming text omits %q:\n%s", want, text)
		}
	}
}

// A session whose claim does not touch the file the evidence policy covers
// is not primed at all.
func TestPrimingIgnoresUnrelatedClaims(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddRepo("app", f.worktree+"/.git", f.worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.WriteMemory(store.MemoryWrite{
		Key: "wire-format", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SetEvidencePolicy(store.EvidenceSet{
		Key: "wire-format", ExpectedMemoryVersion: 1,
		Members: []store.EvidenceMember{{RepoID: "app", BaseOID: "deadbeef",
			Paths: []store.EvidencePath{{Kind: store.PathLiteral, Pattern: "internal/wire"}}}},
		Actor: "r-test", AgentKind: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	f.claim(t, "sess-worker", "docs/readme.md", false)

	if p := CheckPriming("claude-code", "sess-worker", f.worktree); !p.empty() {
		t.Errorf("claim outside the declared scope primed something: %+v", p.Primed)
	}
}

// A glob evidence path primes a claim inside its scope and not one outside
// it — the same "* does not cross /" rule pathglob is unit-tested against,
// exercised here through the actual claim-overlap decision.
func TestPrimingRespectsGlobEvidencePaths(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddRepo("app", f.worktree+"/.git", f.worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.WriteMemory(store.MemoryWrite{
		Key: "glob-primed", Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SetEvidencePolicy(store.EvidenceSet{
		Key: "glob-primed", ExpectedMemoryVersion: 1,
		Members: []store.EvidenceMember{{RepoID: "app", BaseOID: "deadbeef",
			Paths: []store.EvidencePath{{Kind: store.PathGlob, Pattern: "internal/store/*.go"}}}},
		Actor: "r-test", AgentKind: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}

	f.claim(t, "sess-in-scope", "internal/store/links.go", false)
	if p := CheckPriming("claude-code", "sess-in-scope", f.worktree); p.empty() {
		t.Error("a file matching the glob was not primed")
	}

	// A nested file: internal/store/*.go must not cross the / into a subdir.
	f.claim(t, "sess-nested", "internal/store/migrations/x.sql", false)
	if p := CheckPriming("claude-code", "sess-nested", f.worktree); !p.empty() {
		t.Error("* crossed a directory separator and primed a nested file")
	}

	f.claim(t, "sess-elsewhere", "docs/readme.md", false)
	if p := CheckPriming("claude-code", "sess-elsewhere", f.worktree); !p.empty() {
		t.Error("an unrelated claim was primed by a glob evidence path")
	}
}

func TestNoPrimingCostsNothing(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-worker", "src/main.go", false)
	// No evidence policy exists anywhere, so nothing can be primed.
	if p := CheckPriming("claude-code", "sess-worker", f.worktree); !p.empty() {
		t.Errorf("no evidence anywhere still primed: %+v", p.Primed)
	}
	if text := PrimingText(Priming{}); text != "" {
		t.Errorf("an empty Priming rendered %q, want silence", text)
	}
	// An unregistered session has no root and nothing to prime.
	if p := CheckPriming("claude-code", "sess-nobody", f.worktree); !p.empty() {
		t.Errorf("unregistered session primed: %+v", p.Primed)
	}
}

// A session hears about a memory exactly once: the second Stop, with the same
// claim still held, produces nothing.
func TestPrimingDeliveredOnce(t *testing.T) {
	f, _ := primedFixture(t)

	first := CheckPriming("claude-code", "sess-worker", f.worktree)
	if first.empty() {
		t.Fatal("first CheckPriming found nothing to prime")
	}
	MarkPrimed(f.worktree, first)

	if again := CheckPriming("claude-code", "sess-worker", f.worktree); !again.empty() {
		t.Errorf("the same memory was primed twice: %+v", again.Primed)
	}
}

// Checking priming must not consume it, the same rule mail's Stop gate rests
// on: only a caller that actually rendered the text may mark it delivered.
func TestCheckingPrimingDoesNotConsumeIt(t *testing.T) {
	f, _ := primedFixture(t)

	for i := 0; i < 3; i++ {
		if p := CheckPriming("claude-code", "sess-worker", f.worktree); p.empty() {
			t.Fatalf("check %d found nothing — checking alone consumed it", i+1)
		}
	}
}

// MarkPrimed on an empty Priming must be a no-op, so a caller that rendered
// nothing cannot mark a real cue as already shown.
func TestMarkPrimedWithNothingPrimedIsANoop(t *testing.T) {
	f, _ := primedFixture(t)

	MarkPrimed(f.worktree, Priming{})

	if p := CheckPriming("claude-code", "sess-worker", f.worktree); p.empty() {
		t.Error("an empty MarkPrimed call consumed a real cue")
	}
}

// More primed memories than the surfacing cap: the note is capped and reports
// the overflow rather than either crashing or silently dropping the count.
func TestPrimingCapsAndReportsOverflow(t *testing.T) {
	f := newFixture(t)
	f.claim(t, "sess-worker", ".", true)
	if err := f.db.AddRepo("app", f.worktree+"/.git", f.worktree); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxPrimingMemories+3; i++ {
		key := "mem-" + string(rune('a'+i))
		if _, err := f.db.WriteMemory(store.MemoryWrite{
			Key: key, Type: "project", Description: "d", Body: "b", UpdatedBy: "r-test",
		}, "claude-code"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.SetEvidencePolicy(store.EvidenceSet{
			Key: key, ExpectedMemoryVersion: 1,
			Members: []store.EvidenceMember{{RepoID: "app", BaseOID: "deadbeef"}},
			Actor:   "r-test", AgentKind: "claude-code",
		}); err != nil {
			t.Fatal(err)
		}
	}

	p := CheckPriming("claude-code", "sess-worker", f.worktree)
	if len(p.Primed) != store.MaxPrimingMemories {
		t.Fatalf("Primed has %d entries, want capped at %d", len(p.Primed), store.MaxPrimingMemories)
	}
	if p.Overflow != 3 {
		t.Errorf("Overflow = %d, want 3", p.Overflow)
	}
	if text := PrimingText(p); !strings.Contains(text, "3 more") {
		t.Errorf("priming text does not report the overflow count:\n%s", text)
	}
}

// Mail and priming are meant to compose into ONE StopBlock — see hook.go's
// mail-gate, which concatenates exactly this way. Proven here at the level
// this package can test without a full CLI harness.
func TestMailAndPrimingComposeIntoOneNote(t *testing.T) {
	f, _ := primedFixture(t)
	sender := f.register(t, "codex", "sess-sender")
	if _, err := f.db.SendMessage(store.SendRequest{
		FromRoot: sender, ToRoot: mustRootID(t, f, "sess-worker"),
		Subject: "a question", Body: "still there?",
	}); err != nil {
		t.Fatal(err)
	}

	mail := CheckMail("claude-code", "sess-worker", f.worktree)
	priming := CheckPriming("claude-code", "sess-worker", f.worktree)

	text := MailText(mail)
	if pt := PrimingText(priming); pt != "" {
		if text != "" {
			text += "\n"
		}
		text += pt
	}
	if !strings.Contains(text, "a question") {
		t.Error("composed note lost the mail")
	}
	if !strings.Contains(text, "wire-format") {
		t.Error("composed note lost the priming")
	}
}

func mustRootID(t *testing.T, f *fixture, sessionLabel string) string {
	t.Helper()
	r, err := f.db.RootBySession("claude-code", sessionLabel)
	if err != nil {
		t.Fatal(err)
	}
	return r.RootID
}
