package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/store"
)

// Episodes are history, not state (docs/association-model.md §4): what
// happened, distinct from memories' what-is-true. Project scope only —
// episodes are session-bound, and the global database has no roots or
// sessions to bind them to. All three tools are session-root-only (like
// mail, unlike memory_read/memory_search): a peer reports what it found to
// its root, and the root is the one that records it, so what lasts is
// serialized through one accountable actor per session.

// EpisodeRecordInput records, grounds and chains one episode in one call.
type EpisodeRecordInput struct {
	Title      string   `json:"title" jsonschema:"one line: what happened"`
	Body       string   `json:"body" jsonschema:"the record itself — immutable once written; record failures and dead ends, not just successes"`
	MemoryKeys []string `json:"memory_keys,omitempty" jsonschema:"project memories this episode grounds — the semantic claims it is evidence for"`
	Note       string   `json:"note,omitempty" jsonschema:"why this episode grounds memory_keys; required when memory_keys is non-empty, applies to all of them"`
	// CorrectsEpisodeID and ContinuesEpisodeID chain onto an earlier episode.
	// Neither ever rewrites it: what was believed stays written, and this new
	// episode is the later record.
	CorrectsEpisodeID  *int64 `json:"corrects_episode_id,omitempty" jsonschema:"an earlier episode this one corrects"`
	ContinuesEpisodeID *int64 `json:"continues_episode_id,omitempty" jsonschema:"an earlier episode this one resumes"`
}

// EpisodeRecordOutput carries the recorded episode.
type EpisodeRecordOutput struct {
	Episode store.Episode `json:"episode"`
}

func (s *Session) episodeRecord(_ context.Context, _ *mcp.CallToolRequest, in EpisodeRecordInput) (*mcp.CallToolResult, EpisodeRecordOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, EpisodeRecordOutput{}, toolError(err)
	}
	ep, err := s.project.RecordEpisode(store.RecordEpisode{
		Title: in.Title, Body: in.Body, Actor: s.actor(), AgentKind: s.agentKind(),
		MemoryKeys: in.MemoryKeys, Note: in.Note,
		CorrectsEpisodeID: in.CorrectsEpisodeID, ContinuesEpisodeID: in.ContinuesEpisodeID,
	})
	if err != nil {
		return nil, EpisodeRecordOutput{}, toolError(err)
	}
	s.touch()
	return nil, EpisodeRecordOutput{Episode: *ep}, nil
}

// EpisodeReadInput reads one episode.
type EpisodeReadInput struct {
	ID int64 `json:"id"`
}

// EpisodeReadOutput carries the episode's body, provenance and successor
// chain — the chain is ALWAYS present, never behind a flag: superseded
// reasoning is never read without its correction (§5's push principle,
// applied to episodes).
type EpisodeReadOutput struct {
	Found   bool                 `json:"found"`
	Episode *store.EpisodeDetail `json:"episode,omitempty"`
}

func (s *Session) episodeRead(_ context.Context, _ *mcp.CallToolRequest, in EpisodeReadInput) (*mcp.CallToolResult, EpisodeReadOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, EpisodeReadOutput{}, toolError(err)
	}
	detail, err := s.project.ReadEpisode(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, EpisodeReadOutput{Found: false}, nil
	}
	if err != nil {
		return nil, EpisodeReadOutput{}, toolError(err)
	}
	s.touch()
	return nil, EpisodeReadOutput{Found: true, Episode: detail}, nil
}

// EpisodeListInput is the episodic search surface. memory_search deliberately
// stays memories-only (docs/association-model.md §10.2): an extra result
// class interacting with MaxSearchHits would truncate differently than an
// agent expects.
type EpisodeListInput struct {
	Since  string `json:"since,omitempty" jsonschema:"RFC3339; only episodes at or after this — inclusive"`
	Before string `json:"before,omitempty" jsonschema:"RFC3339; only episodes at or before this — inclusive"`
	Query  string `json:"query,omitempty" jsonschema:"free text over title and body; all words must appear"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 20, capped at 50"`
}

// EpisodeListOutput lists episodes recent-first.
type EpisodeListOutput struct {
	Episodes []store.Episode `json:"episodes"`
}

func (s *Session) episodeList(_ context.Context, _ *mcp.CallToolRequest, in EpisodeListInput) (*mcp.CallToolResult, EpisodeListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, EpisodeListOutput{}, toolError(err)
	}
	episodes, err := s.project.ListEpisodes(store.EpisodeQuery{
		Since: in.Since, Before: in.Before, Query: in.Query, Limit: in.Limit,
	})
	if err != nil {
		return nil, EpisodeListOutput{}, toolError(err)
	}
	s.touch()
	return nil, EpisodeListOutput{Episodes: episodes}, nil
}
