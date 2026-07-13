package mcpserver

import (
	"context"
	"errors"

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

// MemorySearchOutput lists hits, project scope first.
type MemorySearchOutput struct {
	Hits []store.SearchHit `json:"hits"`
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
	return nil, MemorySearchOutput{Hits: hits}, nil
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
		return nil, MemoryReadOutput{Found: false}, nil
	}
	if err != nil {
		return nil, MemoryReadOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryReadOutput{Found: true, Memory: m}, nil
}

// MemoryListInput lists a scope's index.
type MemoryListInput struct {
	Scope string `json:"scope" jsonschema:"project or global"`
}

// MemoryListOutput is bodyless on purpose: an index tells an agent what exists
// and what to read, without spending its context on every body.
type MemoryListOutput struct {
	Entries []store.IndexEntry `json:"entries"`
}

func (s *Session) memoryList(_ context.Context, _ *mcp.CallToolRequest, in MemoryListInput) (*mcp.CallToolResult, MemoryListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}
	entries, err := db.ListMemories()
	if err != nil {
		return nil, MemoryListOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryListOutput{Entries: entries}, nil
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
		SourceCommonDir:       s.repo.CommonDir,
	})
	if err != nil {
		return nil, MemoryPromoteOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryPromoteOutput{Created: res.Created, Global: res.Global}, nil
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
	m, err := db.DeleteMemory(in.Key, in.ExpectedVersion, s.actor(), s.agentKind())
	if errors.Is(err, store.ErrNotFound) {
		return nil, MemoryDeleteOutput{}, toolError(serr.E(serr.InvalidInput,
			"memory %q does not exist in the %s scope", in.Key, in.Scope))
	}
	if err != nil {
		return nil, MemoryDeleteOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryDeleteOutput{Deleted: true, Key: m.Key, Version: m.Version}, nil
}
