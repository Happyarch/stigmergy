package mcpserver

import "testing"

func TestEpisodeRecordAndRead(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "wire-format")

	rec := h.call("episode_record", map[string]any{
		"title": "tried compressing the wire format", "body": "it broke backward compatibility.",
		"memory_keys": []string{"wire-format"}, "note": "this is where the lesson came from",
	})
	ep := rec["episode"].(map[string]any)
	id := ep["id"].(float64)
	if id == 0 {
		t.Fatal("episode_record did not assign an id")
	}

	out := h.call("episode_read", map[string]any{"id": id})
	if out["found"] != true {
		t.Fatalf("episode_read found = %v, want true", out["found"])
	}
	detail := out["episode"].(map[string]any)
	if detail["title"] != "tried compressing the wire format" {
		t.Errorf("title = %v", detail["title"])
	}
	grounds := detail["grounds"].([]any)
	if len(grounds) != 1 || grounds[0].(map[string]any)["key"] != "wire-format" {
		t.Errorf("grounds = %v, want [wire-format]", grounds)
	}
	successors := jsonArray(t, detail, "successors")
	if len(successors) != 0 {
		t.Errorf("a fresh episode has successors: %v", successors)
	}
}

func TestEpisodeReadNotFound(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	out := h.call("episode_read", map[string]any{"id": 999})
	if out["found"] != false {
		t.Fatalf("episode_read of a missing id found = %v, want false", out["found"])
	}
}

// A correction always surfaces reading the ORIGINAL episode — the point of
// making episodes immutable and chained rather than editable.
func TestEpisodeCorrectsChainSurfacesOnRead(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	first := h.call("episode_record", map[string]any{"title": "first belief", "body": "X causes Y."})
	firstID := first["episode"].(map[string]any)["id"].(float64)

	h.call("episode_record", map[string]any{
		"title": "correction", "body": "actually it does not.",
		"corrects_episode_id": firstID,
	})

	out := h.call("episode_read", map[string]any{"id": firstID})
	detail := out["episode"].(map[string]any)
	successors := jsonArray(t, detail, "successors")
	if len(successors) != 1 {
		t.Fatalf("successors = %v, want 1", successors)
	}
	if successors[0].(map[string]any)["kind"] != "corrects" {
		t.Errorf("successor kind = %v, want corrects", successors[0].(map[string]any)["kind"])
	}
}

func TestEpisodeListRecentFirstAndQuery(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	h.call("episode_record", map[string]any{"title": "about the wire format", "body": "b"})
	h.call("episode_record", map[string]any{"title": "about the claim guard", "body": "b"})

	out := h.call("episode_list", map[string]any{})
	list := out["episodes"].([]any)
	if len(list) != 2 {
		t.Fatalf("episode_list = %d entries, want 2", len(list))
	}
	// Most recent (second recorded) first.
	if list[0].(map[string]any)["title"] != "about the claim guard" {
		t.Errorf("first entry = %v, want the most recently recorded", list[0])
	}

	filtered := h.call("episode_list", map[string]any{"query": "wire"})
	flist := filtered["episodes"].([]any)
	if len(flist) != 1 || flist[0].(map[string]any)["title"] != "about the wire format" {
		t.Fatalf("query filter = %v, want just the wire-format episode", flist)
	}
}

// memory_read surfaces provenance unconditionally: present as an array, empty
// when nothing grounds it, populated with citations once something does.
func TestMemoryReadProvenance(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "wire-format")

	before := h.call("memory_read", map[string]any{"scope": "project", "key": "wire-format"})
	if prov := jsonArray(t, before, "provenance"); len(prov) != 0 {
		t.Fatalf("provenance before grounding = %v, want empty", prov)
	}

	h.call("episode_record", map[string]any{
		"title": "grounded it", "body": "b",
		"memory_keys": []string{"wire-format"}, "note": "explains the format",
	})

	after := h.call("memory_read", map[string]any{"scope": "project", "key": "wire-format"})
	prov := jsonArray(t, after, "provenance")
	if len(prov) != 1 {
		t.Fatalf("provenance after grounding = %v, want 1 citation", prov)
	}
	if prov[0].(map[string]any)["title"] != "grounded it" {
		t.Errorf("citation = %v", prov[0])
	}

	// Global memories never have episode provenance — episodes are project scope.
	writeMemory(h, "global", "g-fact")
	gout := h.call("memory_read", map[string]any{"scope": "global", "key": "g-fact"})
	if prov := jsonArray(t, gout, "provenance"); len(prov) != 0 {
		t.Errorf("global memory has provenance: %v", prov)
	}
}
