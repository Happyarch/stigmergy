package store

import (
	"testing"
	"time"
)

func recordEpisode(t *testing.T, db *DB, r RecordEpisode) *Episode {
	t.Helper()
	if r.Title == "" {
		r.Title = "an episode"
	}
	if r.Body == "" {
		r.Body = "what happened."
	}
	if r.Actor == "" {
		r.Actor = "r-test"
	}
	if r.AgentKind == "" {
		r.AgentKind = "claude-code"
	}
	ep, err := db.RecordEpisode(r)
	if err != nil {
		t.Fatalf("RecordEpisode: %v", err)
	}
	return ep
}

func TestRecordAndReadEpisode(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")

	ep := recordEpisode(t, db, RecordEpisode{
		Title: "tried approach X", Body: "it failed because Y.",
		MemoryKeys: []string{"alpha"}, Note: "this is where the lesson came from",
	})
	if ep.ID == 0 {
		t.Fatal("RecordEpisode did not assign an id")
	}

	detail, err := db.ReadEpisode(ep.ID)
	if err != nil {
		t.Fatalf("ReadEpisode: %v", err)
	}
	if detail.Title != ep.Title || detail.Body != ep.Body {
		t.Errorf("ReadEpisode = %+v, want it to match what was recorded", detail.Episode)
	}
	if len(detail.Grounds) != 1 || detail.Grounds[0].Key != "alpha" {
		t.Errorf("Grounds = %+v, want [alpha]", detail.Grounds)
	}
	if detail.Grounds[0].Note != "this is where the lesson came from" {
		t.Errorf("grounding note = %q", detail.Grounds[0].Note)
	}
	if len(detail.Successors) != 0 {
		t.Errorf("a fresh episode has successors: %+v", detail.Successors)
	}

	// The audit trail names it.
	var detailStr string
	if err := db.QueryRow(`SELECT detail FROM audit_log WHERE action = 'episode_record'`).Scan(&detailStr); err != nil {
		t.Fatalf("no episode_record audit row: %v", err)
	}
	if detailStr != "tried approach X" {
		t.Errorf("audit detail = %q, want the title", detailStr)
	}
}

func TestReadEpisodeNotFound(t *testing.T) {
	db := testProject(t)
	if _, err := db.ReadEpisode(999); err != ErrNotFound {
		t.Fatalf("ReadEpisode(999) = %v, want ErrNotFound", err)
	}
}

func TestRecordEpisodeValidatesReferences(t *testing.T) {
	db := testProject(t)

	if _, err := db.RecordEpisode(RecordEpisode{
		Title: "t", Body: "b", Actor: "r-test", AgentKind: "claude-code",
		MemoryKeys: []string{"does-not-exist"}, Note: "n",
	}); err == nil {
		t.Fatal("grounding a nonexistent memory was accepted")
	}
	corrects := int64(999)
	if _, err := db.RecordEpisode(RecordEpisode{
		Title: "t", Body: "b", Actor: "r-test", AgentKind: "claude-code",
		CorrectsEpisodeID: &corrects,
	}); err == nil {
		t.Fatal("chaining onto a nonexistent episode was accepted")
	}
}

// A correction chain: reading the ORIGINAL episode must surface the
// correction unconditionally — superseded reasoning is never read alone.
func TestEpisodeReadSurfacesTheSuccessorChain(t *testing.T) {
	db := testProject(t)
	first := recordEpisode(t, db, RecordEpisode{Title: "first belief", Body: "X causes Y."})
	firstID := first.ID
	second := recordEpisode(t, db, RecordEpisode{
		Title: "correction", Body: "actually X does not cause Y.",
		CorrectsEpisodeID: &firstID,
	})
	secondID := second.ID

	detail, err := db.ReadEpisode(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Successors) != 1 || detail.Successors[0].EpisodeID != second.ID {
		t.Fatalf("Successors = %+v, want [%d]", detail.Successors, second.ID)
	}
	if detail.Successors[0].Kind != EpisodeCorrects {
		t.Errorf("successor kind = %q, want %q", detail.Successors[0].Kind, EpisodeCorrects)
	}

	// A second correction, chained onto the first correction, must surface
	// from reading the ORIGINAL too — the whole path, not just one hop.
	third := recordEpisode(t, db, RecordEpisode{
		Title: "further correction", Body: "actually it is more nuanced.",
		CorrectsEpisodeID: &secondID,
	})

	detail, err = db.ReadEpisode(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Successors) != 2 {
		t.Fatalf("multi-hop Successors = %+v, want 2 entries", detail.Successors)
	}
	var sawThird bool
	for _, s := range detail.Successors {
		if s.EpisodeID == third.ID {
			sawThird = true
		}
	}
	if !sawThird {
		t.Errorf("the second-order correction did not surface from the original episode: %+v", detail.Successors)
	}
}

func TestEpisodeContinuesChain(t *testing.T) {
	db := testProject(t)
	first := recordEpisode(t, db, RecordEpisode{Title: "investigation begins", Body: "looked at X."})
	firstID := first.ID
	recordEpisode(t, db, RecordEpisode{
		Title: "investigation resumes", Body: "picked up where it left off.",
		ContinuesEpisodeID: &firstID,
	})

	detail, err := db.ReadEpisode(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Successors) != 1 || detail.Successors[0].Kind != EpisodeContinues {
		t.Fatalf("Successors = %+v, want one 'continues' entry", detail.Successors)
	}
}

func TestListEpisodesRecentFirst(t *testing.T) {
	db := testProject(t)
	var a, b, c *Episode
	at(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), func() {
		a = recordEpisode(t, db, RecordEpisode{Title: "oldest"})
	})
	at(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), func() {
		b = recordEpisode(t, db, RecordEpisode{Title: "middle"})
	})
	at(t, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), func() {
		c = recordEpisode(t, db, RecordEpisode{Title: "newest"})
	})

	list, err := db.ListEpisodes(EpisodeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != c.ID || list[1].ID != b.ID || list[2].ID != a.ID {
		t.Fatalf("ListEpisodes order = %+v, want newest first", list)
	}
}

func TestListEpisodesQueryAndTimeBounds(t *testing.T) {
	db := testProject(t)
	at(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), func() {
		recordEpisode(t, db, RecordEpisode{Title: "about the wire format", Body: "b"})
	})
	at(t, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), func() {
		recordEpisode(t, db, RecordEpisode{Title: "about the claim guard", Body: "b"})
	})

	byQuery, err := db.ListEpisodes(EpisodeQuery{Query: "wire"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byQuery) != 1 || byQuery[0].Title != "about the wire format" {
		t.Fatalf("query filter = %+v, want just the wire-format episode", byQuery)
	}

	byTime, err := db.ListEpisodes(EpisodeQuery{Since: "2026-01-03T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTime) != 1 || byTime[0].Title != "about the claim guard" {
		t.Fatalf("since filter = %+v, want just the later episode", byTime)
	}
}

func TestListEpisodesLimit(t *testing.T) {
	db := testProject(t)
	for i := 0; i < 5; i++ {
		recordEpisode(t, db, RecordEpisode{Title: "e"})
	}
	list, err := db.ListEpisodes(EpisodeQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListEpisodes with Limit:2 returned %d", len(list))
	}
}

// Deleting the memory an episode grounds cascades the provenance row away;
// the episode itself survives — it is history, and stays even once nothing
// cites it. PRAGMA foreign_keys must be ON for this to be enforcement rather
// than a consistency check that happens to pass.
func TestDeleteMemoryCascadesEpisodeProvenance(t *testing.T) {
	db := testProject(t)
	var fkOn int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkOn); err != nil {
		t.Fatal(err)
	}
	if fkOn != 1 {
		t.Fatal("PRAGMA foreign_keys is not ON")
	}

	res, err := write(t, db, "alpha", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	ep := recordEpisode(t, db, RecordEpisode{MemoryKeys: []string{"alpha"}, Note: "n"})

	if _, _, err := db.DeleteMemory("alpha", res.Memory.Version, "r-test", "claude-code"); err != nil {
		t.Fatal(err)
	}

	detail, err := db.ReadEpisode(ep.ID)
	if err != nil {
		t.Fatalf("the episode itself did not survive the memory's deletion: %v", err)
	}
	if len(detail.Grounds) != 0 {
		t.Errorf("provenance survived the memory's deletion: %+v", detail.Grounds)
	}
}

// Deleting an episode cascades its own provenance and chain rows, and does
// not disturb an episode it was chained onto.
func TestDeleteEpisodeCascadesProvenanceAndLinks(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	first := recordEpisode(t, db, RecordEpisode{Title: "first"})
	firstID := first.ID
	second := recordEpisode(t, db, RecordEpisode{
		Title: "second", MemoryKeys: []string{"alpha"}, Note: "n",
		CorrectsEpisodeID: &firstID,
	})

	if _, err := db.Exec(`DELETE FROM episodes WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM episode_memory WHERE episode_id = ?`, second.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("episode_memory survived its episode's deletion")
	}
	if err := db.QueryRow(`SELECT count(*) FROM episode_links WHERE successor_id = ?`, second.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("episode_links survived the successor's deletion")
	}

	// The corrected episode is untouched.
	if _, err := db.ReadEpisode(firstID); err != nil {
		t.Errorf("deleting the successor also removed the episode it corrected: %v", err)
	}

	// episodes_fts must follow the delete too — a broken 'delete' trigger
	// leaves a phantom row that only shows up as a stale query hit.
	list, err := db.ListEpisodes(EpisodeQuery{Query: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("episodes_fts still matches the deleted episode: %v", list)
	}
}

func TestEpisodeProvenanceForBatchedCappedAndTotaled(t *testing.T) {
	db := testProject(t)
	mustWrite(t, db, "alpha")
	for i := 0; i < MaxProvenanceSurfaced+2; i++ {
		recordEpisode(t, db, RecordEpisode{Title: "e", MemoryKeys: []string{"alpha"}, Note: "n"})
	}

	prov, err := db.EpisodeProvenanceFor([]string{"alpha", "no-citations"})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := prov["alpha"]
	if !ok {
		t.Fatal("alpha has no provenance entry")
	}
	if len(entry.Citations) != MaxProvenanceSurfaced {
		t.Fatalf("Citations = %d, want capped at %d", len(entry.Citations), MaxProvenanceSurfaced)
	}
	if entry.Total != MaxProvenanceSurfaced+2 {
		t.Errorf("Total = %d, want %d", entry.Total, MaxProvenanceSurfaced+2)
	}
	if _, ok := prov["no-citations"]; ok {
		t.Error("a memory with no citations got an entry")
	}
}
