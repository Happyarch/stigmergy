package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// SuggestionLimit is how many near-duplicate entries a create-path write
// reports back. Enough to notice you are about to fork an existing memory;
// few enough not to bury the result.
const SuggestionLimit = 3

// MemorySearchInput queries the shared memory.
type MemorySearchInput struct {
	Query  string   `json:"query" jsonschema:"free text; all words must appear"`
	Scopes []string `json:"scopes,omitempty" jsonschema:"scopes to search: project, global, or both (default)"`
}

// SearchHitEntry is one search hit, carrying bounded neighbor stubs.
//
// store.SearchHit is embedded rather than copied, so the shape agents already
// parse is unchanged — links are purely additive. Linked is never omitted:
// an omitted field would be indistinguishable from "links were never asked
// for", which does not apply here — they are pushed unconditionally (§5 of
// docs/association-model.md), so an empty array means "this hit has none",
// not "nobody asked".
type SearchHitEntry struct {
	store.SearchHit
	Linked []store.Neighbor `json:"linked"`
}

// MemorySearchOutput lists hits, project scope first.
type MemorySearchOutput struct {
	Hits []SearchHitEntry `json:"hits"`
}

func (s *Session) memorySearch(_ context.Context, _ *mcp.CallToolRequest, in MemorySearchInput) (*mcp.CallToolResult, MemorySearchOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, MemorySearchOutput{}, toolError(err)
	}
	scopes, err := store.ParseScopes(in.Scopes)
	if err != nil {
		return nil, MemorySearchOutput{}, toolError(err)
	}
	hits, err := store.SearchScopes(s.project, s.global, in.Query, scopes)
	if err != nil {
		return nil, MemorySearchOutput{}, toolError(err)
	}
	s.touch()

	// One batched NeighborsOf per scope, not one per hit — the same reasoning
	// as EvidencePolicies: an opt-in nobody can afford to set is the same as
	// not having it, and push-not-pull exposure has to stay affordable.
	var projectKeys, globalKeys []string
	for _, h := range hits {
		if h.Scope == string(store.Project) {
			projectKeys = append(projectKeys, h.Key)
		} else {
			globalKeys = append(globalKeys, h.Key)
		}
	}
	var projectNeighbors, globalNeighbors map[string][]store.Neighbor
	if len(projectKeys) > 0 {
		projectNeighbors, err = s.project.NeighborsOf(projectKeys)
		if err != nil {
			return nil, MemorySearchOutput{}, toolError(err)
		}
	}
	if len(globalKeys) > 0 && s.global != nil {
		globalNeighbors, err = s.global.NeighborsOf(globalKeys)
		if err != nil {
			return nil, MemorySearchOutput{}, toolError(err)
		}
	}

	out := MemorySearchOutput{Hits: make([]SearchHitEntry, 0, len(hits))}
	for _, h := range hits {
		e := SearchHitEntry{SearchHit: h, Linked: []store.Neighbor{}}
		neighbors := projectNeighbors
		if h.Scope != string(store.Project) {
			neighbors = globalNeighbors
		}
		if list := neighbors[h.Key]; len(list) > 0 {
			if len(list) > store.MaxSearchNeighbors {
				list = list[:store.MaxSearchNeighbors]
			}
			e.Linked = list
		}
		out.Hits = append(out.Hits, e)
	}
	return nil, out, nil
}

// MemoryReadInput reads one entry.
type MemoryReadInput struct {
	Scope string `json:"scope" jsonschema:"project or global"`
	Key   string `json:"key"`
}

// MemoryReadOutput carries the entry, or reports its absence. A missing memory
// is a normal answer, not an error: agents ask about keys that may not exist.
type MemoryReadOutput struct {
	Found  bool          `json:"found"`
	Memory *store.Memory `json:"memory,omitempty"`
	// Links is always present when the memory is found — an empty array when
	// there are none, never omitted. Omission would be indistinguishable from
	// "links were never asked for", which cannot happen here: they are pushed
	// unconditionally on every read (docs/association-model.md §5). No
	// include_links flag exists, by design (Appendix A.7).
	Links []store.Neighbor `json:"links"`
	// LinksTotal is set only when the list above was truncated to
	// store.MaxNeighborsSurfaced.
	LinksTotal int `json:"links_total,omitempty"`
}

func (s *Session) memoryRead(_ context.Context, _ *mcp.CallToolRequest, in MemoryReadInput) (*mcp.CallToolResult, MemoryReadOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, MemoryReadOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryReadOutput{}, toolError(err)
	}
	m, err := db.ReadMemory(in.Key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, MemoryReadOutput{Found: false, Links: []store.Neighbor{}}, nil
	}
	if err != nil {
		return nil, MemoryReadOutput{}, toolError(err)
	}
	neighbors, err := db.NeighborsOf([]string{in.Key})
	if err != nil {
		return nil, MemoryReadOutput{}, toolError(err)
	}
	s.touch()
	out := MemoryReadOutput{Found: true, Memory: m, Links: []store.Neighbor{}}
	if list := neighbors[in.Key]; len(list) > 0 {
		if len(list) > store.MaxNeighborsSurfaced {
			out.LinksTotal = len(list)
			list = list[:store.MaxNeighborsSurfaced]
		}
		out.Links = list
	}
	return nil, out, nil
}

// MemoryListInput lists a scope's index.
//
// The time bounds are on last MUTATION — the only time this schema records.
// They answer "what has nobody touched in a while?", which is a question worth
// asking during upkeep. They do not answer "what has gone stale": a memory can
// be untouched for a year and still be true, and edited an hour ago without
// anyone having checked it.
type MemoryListInput struct {
	Scope         string `json:"scope" jsonschema:"project or global"`
	UpdatedSince  string `json:"updated_since,omitempty" jsonschema:"RFC3339; only entries last changed at or after this"`
	UpdatedBefore string `json:"updated_before,omitempty" jsonschema:"RFC3339; only entries last changed at or before this"`
	OrderBy       string `json:"order_by,omitempty" jsonschema:"key (default) or recent (most recently changed first)"`
	// IncludeDrift is named for what it returns. "include_freshness" would
	// promise a verdict nothing here emits.
	IncludeDrift bool `json:"include_drift,omitempty" jsonschema:"project only; report what has CHANGED in each memory's declared scope. Not a freshness verdict"`
	// IncludeVerification reports when anyone last CHECKED each memory, which is
	// a different question from when it last changed.
	IncludeVerification bool `json:"include_verification,omitempty" jsonschema:"project only; the last recorded verification outcome and the counts so far"`
}

// MemoryListEntry is an index entry, optionally carrying change evidence.
//
// store.IndexEntry is embedded rather than copied, so its JSON fields are
// promoted and the shape agents already parse is unchanged — evidence is purely
// additive.
type MemoryListEntry struct {
	store.IndexEntry
	Evidence *EvidenceInfo `json:"evidence,omitempty"`
	// Verification is the last time anyone said they checked this, and what they
	// concluded. Like Evidence it is always present when asked for, so "nobody
	// has ever checked" is a value rather than a missing field.
	Verification *store.VerificationSummary `json:"verification,omitempty"`
	// LinkCount is a structure signal only — no bodies, no reasons, just how
	// many associations this memory has. A high count is worth a look with
	// memory_read, not a warning by itself.
	LinkCount int `json:"link_count"`
}

// MemoryListOutput is bodyless on purpose: an index tells an agent what exists
// and what to read, without spending its context on every body.
type MemoryListOutput struct {
	Entries []MemoryListEntry `json:"entries"`
}

func (s *Session) memoryList(ctx context.Context, _ *mcp.CallToolRequest, in MemoryListInput) (*mcp.CallToolResult, MemoryListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}
	if in.IncludeDrift && db.Kind != store.Project {
		return nil, MemoryListOutput{}, toolError(evidenceUnsupported(in.Scope))
	}
	if in.IncludeVerification && db.Kind != store.Project {
		return nil, MemoryListOutput{}, toolError(verificationUnsupported(in.Scope))
	}
	entries, err := db.QueryMemories(store.MemoryQuery{
		UpdatedSince:  in.UpdatedSince,
		UpdatedBefore: in.UpdatedBefore,
		OrderBy:       in.OrderBy,
	})
	if err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}

	out := MemoryListOutput{Entries: make([]MemoryListEntry, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, MemoryListEntry{IndexEntry: e})
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	if counts, err := db.LinkCounts(keys); err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	} else {
		for i := range out.Entries {
			out.Entries[i].LinkCount = counts[out.Entries[i].Key]
		}
	}
	if in.IncludeDrift {
		policies, err := db.EvidencePolicies()
		if err != nil {
			return nil, MemoryListOutput{}, toolError(err)
		}
		evidence := s.evaluateEvidence(ctx, policies, keys)
		for i := range out.Entries {
			// Every entry gets a record, including those with no policy. An
			// omitted field would be indistinguishable from "drift was never
			// asked for", which is the one ambiguity this flag exists to remove.
			if ev, ok := evidence[out.Entries[i].Key]; ok {
				out.Entries[i].Evidence = ev
			} else {
				out.Entries[i].Evidence = notConfigured()
			}
		}
	}
	if in.IncludeVerification {
		summaries, err := db.VerificationSummaries()
		if err != nil {
			return nil, MemoryListOutput{}, toolError(err)
		}
		for i := range out.Entries {
			// A memory nobody has ever checked reports an empty summary rather
			// than nothing at all — the same rule as evidence, for the same
			// reason: silence is indistinguishable from "never asked".
			if sum, ok := summaries[out.Entries[i].Key]; ok {
				out.Entries[i].Verification = sum
			} else {
				out.Entries[i].Verification = &store.VerificationSummary{}
			}
		}
	}
	s.touch()
	return nil, out, nil
}

// MemoryWriteInput is a compare-and-swap write.
type MemoryWriteInput struct {
	Scope           string `json:"scope" jsonschema:"project or global"`
	Key             string `json:"key" jsonschema:"lowercase kebab-case identifier"`
	Type            string `json:"type" jsonschema:"user, feedback, project, or reference"`
	Description     string `json:"description" jsonschema:"one line; this is what other agents see when listing"`
	Body            string `json:"body"`
	ExpectedVersion *int   `json:"expected_version,omitempty" jsonschema:"omit to create a new memory; pass the current version to update one"`
}

// MemoryWriteOutput reports the result, plus near-duplicates on create.
type MemoryWriteOutput struct {
	Created bool          `json:"created"`
	Memory  *store.Memory `json:"memory"`
	// Similar is populated only when a memory was created: it is the moment to
	// notice that an entry covering this ground already exists under another
	// key, before the two versions of the truth drift apart.
	Similar []store.IndexEntry `json:"similar,omitempty"`
	// Note appears only on an update to a memory that has an evidence policy,
	// to say that its baseline was deliberately left alone.
	Note string `json:"note,omitempty"`
}

func (s *Session) memoryWrite(_ context.Context, _ *mcp.CallToolRequest, in MemoryWriteInput) (*mcp.CallToolResult, MemoryWriteOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryWriteOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryWriteOutput{}, toolError(err)
	}

	// Look for near-duplicates before writing: afterwards the new entry would
	// match its own text and drown out the very entries worth warning about.
	var similar []store.IndexEntry
	if in.ExpectedVersion == nil {
		similar, _ = db.SuggestSimilar(in.Key+" "+in.Description, SuggestionLimit)
	}

	res, err := db.WriteMemory(store.MemoryWrite{
		Key: in.Key, Type: in.Type, Description: in.Description, Body: in.Body,
		UpdatedBy: s.actor(), ExpectedVersion: in.ExpectedVersion,
	}, s.agentKind())
	if err != nil {
		return nil, MemoryWriteOutput{}, toolError(err)
	}
	s.touch()

	out := MemoryWriteOutput{Created: res.Created, Memory: res.Memory}
	if res.Created {
		for _, e := range similar {
			if e.Key != res.Memory.Key {
				out.Similar = append(out.Similar, e)
			}
		}
	}
	// An update to a memory that has an evidence policy says so. A create never
	// can — the policy is attached afterwards — and a memory without one has
	// nothing to report, so the note stays rare enough to be read.
	if !res.Created && db.Kind == store.Project && db.HasEvidencePolicy(res.Memory.Key) {
		out.Note = baselineNote(res.Memory.Key)
	}
	return nil, out, nil
}

// MemoryPromoteInput copies a project memory into the global scope.
type MemoryPromoteInput struct {
	Key                   string `json:"key" jsonschema:"project memory to promote"`
	ExpectedVersion       int    `json:"expected_version" jsonschema:"current version of the project memory"`
	GlobalKey             string `json:"global_key,omitempty" jsonschema:"key to use in the global scope; defaults to the same key"`
	ExpectedGlobalVersion *int   `json:"expected_global_version,omitempty" jsonschema:"omit if the global key is new; pass its current version to overwrite"`
}

// MemoryPromoteOutput reports the resulting global entry.
type MemoryPromoteOutput struct {
	Created bool          `json:"created"`
	Global  *store.Memory `json:"global"`
	// Note appears when the source had an evidence policy, which is not copied.
	Note string `json:"note,omitempty"`
}

func (s *Session) memoryPromote(_ context.Context, _ *mcp.CallToolRequest, in MemoryPromoteInput) (*mcp.CallToolResult, MemoryPromoteOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryPromoteOutput{}, toolError(err)
	}
	res, err := store.PromoteMemory(s.project, s.global, store.Promote{
		Key:                   in.Key,
		ExpectedVersion:       in.ExpectedVersion,
		GlobalKey:             in.GlobalKey,
		ExpectedGlobalVersion: in.ExpectedGlobalVersion,
		Actor:                 s.actor(),
		AgentKind:             s.agentKind(),
		SourceCommonDir:       s.commonDir(),
	})
	if err != nil {
		return nil, MemoryPromoteOutput{}, toolError(err)
	}
	s.touch()
	out := MemoryPromoteOutput{Created: res.Created, Global: res.Global}
	// Promotion copies content only. An evidence policy is project-local
	// observation configuration and not part of what the memory asserts, so it
	// stays on the source — and git evidence is undefined in the global scope
	// anyway. The wording is "not copied", never "lost".
	var notes []string
	if s.project.HasEvidencePolicy(in.Key) {
		notes = append(notes, fmt.Sprintf(
			"Content promoted. Git evidence is project-only and was not copied; the source policy remains on project:%s.",
			in.Key))
	}
	// Links are same-scope only (docs/association-model.md §1): a project link
	// names project memories, so it has no meaning against the global copy.
	if counts, err := s.project.LinkCounts([]string{in.Key}); err == nil && counts[in.Key] > 0 {
		notes = append(notes,
			"Links are project-local and were not copied; re-link the global copy against global memories if the associations hold there.")
	}
	if len(notes) > 0 {
		out.Note = strings.Join(notes, " ")
	}
	return nil, out, nil
}

// MemoryDeleteInput removes an entry for good.
type MemoryDeleteInput struct {
	Scope           string `json:"scope" jsonschema:"project or global"`
	Key             string `json:"key"`
	ExpectedVersion int    `json:"expected_version" jsonschema:"the current version; a delete must never race an update you have not seen"`
}

// MemoryDeleteOutput reports what was removed.
type MemoryDeleteOutput struct {
	Deleted bool   `json:"deleted"`
	Key     string `json:"key"`
	Version int    `json:"version"`
	// SeveredLinks names the memories this one was linked to, if any. They are
	// severed, never lost: the memory that vanished is what stopped existing,
	// not the fact that its neighbors were once associated with it.
	SeveredLinks []string `json:"severed_links,omitempty"`
	Note         string   `json:"note,omitempty"`
}

func (s *Session) memoryDelete(_ context.Context, _ *mcp.CallToolRequest, in MemoryDeleteInput) (*mcp.CallToolResult, MemoryDeleteOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryDeleteOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryDeleteOutput{}, toolError(err)
	}
	m, severed, err := db.DeleteMemory(in.Key, in.ExpectedVersion, s.actor(), s.agentKind())
	if errors.Is(err, store.ErrNotFound) {
		return nil, MemoryDeleteOutput{}, toolError(serr.E(serr.InvalidInput,
			"memory %q does not exist in the %s scope", in.Key, in.Scope))
	}
	if err != nil {
		return nil, MemoryDeleteOutput{}, toolError(err)
	}
	s.touch()
	out := MemoryDeleteOutput{Deleted: true, Key: m.Key, Version: m.Version}
	if len(severed) > 0 {
		out.SeveredLinks = severed
		out.Note = fmt.Sprintf("%d link(s) to %q were severed, not lost: %s.",
			len(severed), m.Key, strings.Join(severed, ", "))
	}
	return nil, out, nil
}
