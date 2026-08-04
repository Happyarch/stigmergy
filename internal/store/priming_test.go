package store

import (
	"testing"

	"github.com/happyarch/stigmergy/internal/ids"
)

func mustRootForPriming(t *testing.T, db *DB, sessionLabel string) string {
	t.Helper()
	root, _, err := db.RegisterRoot(Registration{
		RootID: ids.NewRootID(), AgentKind: "claude-code",
		Worktree: "/wt", SessionLabel: sessionLabel,
	})
	if err != nil {
		t.Fatal(err)
	}
	return root.RootID
}

func TestMarkPrimedIsIdempotentAndScopedPerRoot(t *testing.T) {
	db := testProject(t)
	rootA := mustRootForPriming(t, db, "sess-a")
	rootB := mustRootForPriming(t, db, "sess-b")
	mustWrite(t, db, "alpha")
	mustWrite(t, db, "beta")

	if err := db.MarkPrimed(rootA, []string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}
	// Marking again must not error — a retried Stop hook should not fail.
	if err := db.MarkPrimed(rootA, []string{"alpha"}); err != nil {
		t.Fatalf("re-marking an already-primed key errored: %v", err)
	}

	delivered, err := db.PrimingDelivered(rootA, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if !delivered["alpha"] || !delivered["beta"] {
		t.Errorf("PrimingDelivered(rootA) = %+v, want both marked", delivered)
	}

	// Scoped per root: rootB has seen nothing.
	deliveredB, err := db.PrimingDelivered(rootB, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if deliveredB["alpha"] || deliveredB["beta"] {
		t.Errorf("PrimingDelivered(rootB) = %+v, want neither marked — priming is per root", deliveredB)
	}
}

func TestMarkPrimedNoopOnEmptyInput(t *testing.T) {
	db := testProject(t)
	if err := db.MarkPrimed("", []string{"alpha"}); err != nil {
		t.Fatalf("MarkPrimed with no root errored: %v", err)
	}
	root := mustRootForPriming(t, db, "sess-a")
	if err := db.MarkPrimed(root, nil); err != nil {
		t.Fatalf("MarkPrimed with no keys errored: %v", err)
	}
}

// A root's priming history is bookkeeping about that root, not history worth
// keeping past it — it cascades away with the root, same as claims do.
func TestPrimingDeliveredCascadesWithTheRoot(t *testing.T) {
	db := testProject(t)
	root := mustRootForPriming(t, db, "sess-a")
	mustWrite(t, db, "alpha")
	if err := db.MarkPrimed(root, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`DELETE FROM roots WHERE root_id = ?`, root); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM priming_delivered WHERE root_id = ?`, root).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d priming_delivered rows survived the root's deletion", n)
	}
}
