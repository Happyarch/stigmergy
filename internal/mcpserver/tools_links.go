package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/store"
)

// MemoryLinkInput asserts an association between two memories in one scope.
type MemoryLinkInput struct {
	Scope    string `json:"scope" jsonschema:"project or global"`
	Key      string `json:"key"`
	OtherKey string `json:"other_key" jsonschema:"the memory to associate with key, in the same scope"`
	Reason   string `json:"reason" jsonschema:"why this association matters — the encoding context a future reader needs; mandatory, one line"`
}

// MemoryLinkOutput carries the stored, canonically-ordered edge.
type MemoryLinkOutput struct {
	Link store.Link `json:"link"`
}

func (s *Session) memoryLink(_ context.Context, _ *mcp.CallToolRequest, in MemoryLinkInput) (*mcp.CallToolResult, MemoryLinkOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryLinkOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryLinkOutput{}, toolError(err)
	}
	link, err := db.CreateLink(in.Key, in.OtherKey, in.Reason, s.actor(), s.agentKind())
	if err != nil {
		return nil, MemoryLinkOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryLinkOutput{Link: link}, nil
}

// MemoryUnlinkInput severs an association.
type MemoryUnlinkInput struct {
	Scope    string `json:"scope" jsonschema:"project or global"`
	Key      string `json:"key"`
	OtherKey string `json:"other_key"`
}

// MemoryUnlinkOutput reports whether a link was actually removed. false is
// not an error: unlinking something already gone is routine.
type MemoryUnlinkOutput struct {
	Removed bool `json:"removed"`
}

func (s *Session) memoryUnlink(_ context.Context, _ *mcp.CallToolRequest, in MemoryUnlinkInput) (*mcp.CallToolResult, MemoryUnlinkOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryUnlinkOutput{}, toolError(err)
	}
	db, err := s.scopeDB(in.Scope)
	if err != nil {
		return nil, MemoryUnlinkOutput{}, toolError(err)
	}
	removed, err := db.DeleteLink(in.Key, in.OtherKey, s.actor(), s.agentKind())
	if err != nil {
		return nil, MemoryUnlinkOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryUnlinkOutput{Removed: removed}, nil
}
