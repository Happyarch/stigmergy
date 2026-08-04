package mcpserver

import "testing"

// jsonArray asserts a field decoded from a tool's JSON body is present as an
// array (possibly empty), not omitted or null. json.Unmarshal into
// map[string]any turns a JSON null into a nil interface and a JSON "[]" into
// an empty []any — the type assertion is what actually tells them apart, a
// Go zero-value check would not.
func jsonArray(t *testing.T, out map[string]any, field string) []any {
	t.Helper()
	v, present := out[field]
	if !present {
		t.Fatalf("field %q is missing entirely", field)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("field %q is %#v (probably JSON null), want a JSON array", field, v)
	}
	return arr
}

func writeMemory(h *harness, scope, key string) {
	h.t.Helper()
	h.call("memory_write", map[string]any{
		"scope": scope, "key": key, "type": "project",
		"description": "about " + key, "body": "body for " + key,
	})
}

func TestMemoryReadLinksPresentAsEmptyArrayNotNull(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "alpha")

	out := h.call("memory_read", map[string]any{"scope": "project", "key": "alpha"})
	jsonArray(t, out, "links")
}

func TestMemoryLinkAndUnlinkRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "alpha")
	writeMemory(h, "project", "beta")

	h.call("memory_link", map[string]any{
		"scope": "project", "key": "alpha", "other_key": "beta", "reason": "share the dispatch table",
	})

	// Surfaces from EITHER endpoint, unconditionally, on read.
	out := h.call("memory_read", map[string]any{"scope": "project", "key": "beta"})
	links := jsonArray(t, out, "links")
	if len(links) != 1 {
		t.Fatalf("beta's links = %v, want 1", links)
	}
	first := links[0].(map[string]any)
	if first["key"] != "alpha" || first["reason"] != "share the dispatch table" {
		t.Errorf("neighbor = %#v, want key=alpha reason=share the dispatch table", first)
	}

	// And on search.
	sres := h.call("memory_search", map[string]any{"query": "alpha", "scopes": []string{"project"}})
	hits := sres["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("search found no hits for alpha")
	}
	hit := hits[0].(map[string]any)
	linked := jsonArray(t, hit, "linked")
	if len(linked) != 1 || linked[0].(map[string]any)["key"] != "beta" {
		t.Errorf("search hit's linked = %v, want [beta]", linked)
	}

	// link_count on list.
	lres := h.call("memory_list", map[string]any{"scope": "project"})
	for _, e := range lres["entries"].([]any) {
		entry := e.(map[string]any)
		if entry["key"] == "alpha" && entry["link_count"] != float64(1) {
			t.Errorf("alpha's link_count = %v, want 1", entry["link_count"])
		}
	}

	unlinkOut := h.call("memory_unlink", map[string]any{"scope": "project", "key": "beta", "other_key": "alpha"})
	if unlinkOut["removed"] != true {
		t.Fatalf("memory_unlink removed = %v, want true", unlinkOut["removed"])
	}
	out = h.call("memory_read", map[string]any{"scope": "project", "key": "alpha"})
	if links := jsonArray(t, out, "links"); len(links) != 0 {
		t.Errorf("links survived unlink: %v", links)
	}

	// Unlinking again is not an error; it just reports false.
	unlinkOut = h.call("memory_unlink", map[string]any{"scope": "project", "key": "beta", "other_key": "alpha"})
	if unlinkOut["removed"] != false {
		t.Errorf("second unlink removed = %v, want false", unlinkOut["removed"])
	}
}

func TestMemoryLinkDuplicateCarriesExistingEdge(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "alpha")
	writeMemory(h, "project", "beta")

	h.call("memory_link", map[string]any{"scope": "project", "key": "alpha", "other_key": "beta", "reason": "first"})
	_, _, errBody := h.tryCall("memory_link", map[string]any{"scope": "project", "key": "beta", "other_key": "alpha", "reason": "second"})
	if errBody == nil || errBody["code"] != "cas_conflict" {
		t.Fatalf("duplicate link error = %#v, want cas_conflict", errBody)
	}
	if errBody["current"] == nil {
		t.Errorf("duplicate link error does not carry the existing edge: %#v", errBody)
	}
}

func TestMemoryDeleteReportsSeveredLinks(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "alpha")
	writeMemory(h, "project", "beta")
	h.call("memory_link", map[string]any{"scope": "project", "key": "alpha", "other_key": "beta", "reason": "reason"})

	out := h.call("memory_delete", map[string]any{"scope": "project", "key": "alpha", "expected_version": 1})
	severed, ok := out["severed_links"].([]any)
	if !ok || len(severed) != 1 || severed[0] != "beta" {
		t.Fatalf("severed_links = %#v, want [beta]", out["severed_links"])
	}
	note, _ := out["note"].(string)
	if note == "" {
		t.Error("no note naming what was severed")
	}
}

func TestMemoryPromoteLeavesLinksOnSourceAndNotesIt(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "alpha")
	writeMemory(h, "project", "beta")
	h.call("memory_link", map[string]any{"scope": "project", "key": "alpha", "other_key": "beta", "reason": "reason"})

	out := h.call("memory_promote", map[string]any{"key": "alpha", "expected_version": 1})
	note, _ := out["note"].(string)
	if note == "" {
		t.Fatal("memory_promote with a linked source produced no note")
	}

	// The link is still on the project source.
	read := h.call("memory_read", map[string]any{"scope": "project", "key": "alpha"})
	if links := jsonArray(t, read, "links"); len(links) != 1 {
		t.Errorf("source lost its link on promote: %v", links)
	}
	// And was not copied to the global side.
	gread := h.call("memory_read", map[string]any{"scope": "global", "key": "alpha"})
	if links := jsonArray(t, gread, "links"); len(links) != 0 {
		t.Errorf("global copy has links it should not: %v", links)
	}
}

// A hub with more neighbors than the surfacing caps allow must truncate and
// report the true total — not just avoid a crash. This is the fan-effect
// counterweight (docs/association-model.md §2), and it is the one thing that
// stands in for the weights this design deliberately refuses to store.
func TestMemoryReadLinksTruncatedWithTotal(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "hub")
	for i := 0; i < 9; i++ {
		key := "spoke-" + string(rune('a'+i))
		writeMemory(h, "project", key)
		h.call("memory_link", map[string]any{"scope": "project", "key": "hub", "other_key": key, "reason": "reason"})
	}

	out := h.call("memory_read", map[string]any{"scope": "project", "key": "hub"})
	links := jsonArray(t, out, "links")
	if len(links) != 8 {
		t.Fatalf("memory_read links = %d, want capped at 8", len(links))
	}
	total, _ := out["links_total"].(float64)
	if int(total) != 9 {
		t.Errorf("links_total = %v, want 9", out["links_total"])
	}
}

func TestMemorySearchLinkedCappedAtFour(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "project", "hub")
	for i := 0; i < 6; i++ {
		key := "spoke-" + string(rune('a'+i))
		writeMemory(h, "project", key)
		h.call("memory_link", map[string]any{"scope": "project", "key": "hub", "other_key": key, "reason": "reason"})
	}

	sres := h.call("memory_search", map[string]any{"query": "hub", "scopes": []string{"project"}})
	hits := sres["hits"].([]any)
	var hub map[string]any
	for _, e := range hits {
		hit := e.(map[string]any)
		if hit["key"] == "hub" {
			hub = hit
		}
	}
	if hub == nil {
		t.Fatal("search did not find hub")
	}
	linked := jsonArray(t, hub, "linked")
	if len(linked) != 4 {
		t.Fatalf("search hit's linked = %d, want capped at 4", len(linked))
	}
}

func TestMemoryLinkGlobalScope(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	writeMemory(h, "global", "g-alpha")
	writeMemory(h, "global", "g-beta")

	h.call("memory_link", map[string]any{"scope": "global", "key": "g-alpha", "other_key": "g-beta", "reason": "same machine fact"})
	out := h.call("memory_read", map[string]any{"scope": "global", "key": "g-beta"})
	if links := jsonArray(t, out, "links"); len(links) != 1 {
		t.Fatalf("global link not surfaced: %v", links)
	}
}
