package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/paths"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// ClaimAcquireInput reserves a path.
type ClaimAcquireInput struct {
	ScopePath  string `json:"scope_path" jsonschema:"path relative to the repository root; \".\" is the whole repo"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"true to claim a directory and everything under it"`
	Reason     string `json:"reason" jsonschema:"what you are doing; other agents read this when your claim blocks them"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"how long you need it (60-86400; default 1800)"`
}

// ClaimOutput carries a claim.
type ClaimOutput struct {
	Claim *store.Claim `json:"claim"`
}

func (s *Session) claimAcquire(_ context.Context, _ *mcp.CallToolRequest, in ClaimAcquireInput) (*mcp.CallToolResult, ClaimOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	scope, err := paths.ValidateScope(in.ScopePath)
	if err != nil {
		return nil, ClaimOutput{}, toolError(serr.E(serr.InvalidInput, "%s", err.Error()))
	}
	// The repo root is only meaningful as a subtree: a non-recursive claim on
	// "." would name a directory as if it were a file, and match no edit.
	if scope == "." {
		in.Recursive = true
	}

	claim, err := s.project.AcquireClaim(store.ClaimRequest{
		ScopePath: scope, Recursive: in.Recursive, RootID: s.actor(),
		Worktree: s.root.Worktree, Branch: s.root.Branch,
		Reason: in.Reason, TTLSeconds: in.TTLSeconds,
	})
	if err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	s.touch()
	return nil, ClaimOutput{Claim: claim}, nil
}

// ClaimCheckInput asks whether a path is claimed.
type ClaimCheckInput struct {
	Path string `json:"path" jsonschema:"the file you intend to edit; absolute, or relative to the worktree root"`
}

// ClaimCheckOutput reports the claims covering a path. Each carries own: an
// agent blocked by its own claim should just proceed, not negotiate with itself.
type ClaimCheckOutput struct {
	Path    string        `json:"path"`
	Claimed bool          `json:"claimed"`
	Claims  []store.Claim `json:"claims,omitempty"`
}

func (s *Session) claimCheck(_ context.Context, _ *mcp.CallToolRequest, in ClaimCheckInput) (*mcp.CallToolResult, ClaimCheckOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, ClaimCheckOutput{}, toolError(err)
	}
	rel, err := paths.Normalize(s.repo.WorktreeRoot, s.repo.WorktreeRoot, in.Path)
	if errors.Is(err, paths.ErrOutsideWorktree) {
		// Claims only govern the repository. Outside it, there is nothing to
		// report and nothing to block.
		return nil, ClaimCheckOutput{Path: in.Path, Claimed: false}, nil
	}
	if err != nil {
		return nil, ClaimCheckOutput{}, toolError(serr.E(serr.InvalidInput, "%s", err.Error()))
	}

	found, err := s.project.ClaimsCovering(rel, s.actor())
	if err != nil {
		return nil, ClaimCheckOutput{}, toolError(err)
	}
	s.touch()
	return nil, ClaimCheckOutput{Path: rel, Claimed: len(found) > 0, Claims: found}, nil
}

// ClaimListOutput lists every claim in force.
type ClaimListOutput struct {
	Claims []store.Claim `json:"claims"`
}

func (s *Session) claimListActive(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ClaimListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, ClaimListOutput{}, toolError(err)
	}
	list, err := s.project.ActiveClaims(s.actor())
	if err != nil {
		return nil, ClaimListOutput{}, toolError(err)
	}
	s.touch()
	return nil, ClaimListOutput{Claims: list}, nil
}

// ClaimRenewInput extends one of your claims.
type ClaimRenewInput struct {
	ClaimID    int64 `json:"claim_id"`
	TTLSeconds int   `json:"ttl_seconds,omitempty" jsonschema:"new lifetime from now (60-86400; default 1800)"`
}

func (s *Session) claimRenew(_ context.Context, _ *mcp.CallToolRequest, in ClaimRenewInput) (*mcp.CallToolResult, ClaimOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	claim, err := s.project.RenewClaim(in.ClaimID, s.actor(), in.TTLSeconds)
	if err != nil {
		return nil, ClaimOutput{}, toolError(claimErr(err, in.ClaimID))
	}
	s.touch()
	return nil, ClaimOutput{Claim: claim}, nil
}

// ClaimReleaseInput gives a claim back.
type ClaimReleaseInput struct {
	ClaimID int64 `json:"claim_id"`
}

// ClaimReleaseOutput confirms the release.
type ClaimReleaseOutput struct {
	ClaimID  int64 `json:"claim_id"`
	Released bool  `json:"released"`
}

func (s *Session) claimRelease(_ context.Context, _ *mcp.CallToolRequest, in ClaimReleaseInput) (*mcp.CallToolResult, ClaimReleaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimReleaseOutput{}, toolError(err)
	}
	if err := s.project.ReleaseClaim(in.ClaimID, s.actor()); err != nil {
		return nil, ClaimReleaseOutput{}, toolError(claimErr(err, in.ClaimID))
	}
	s.touch()
	return nil, ClaimReleaseOutput{ClaimID: in.ClaimID, Released: true}, nil
}

// claimErr turns a vanished claim into something the agent can act on: it
// expired or was already released, so the path is free and there is nothing to
// renew or hand back.
func claimErr(err error, id int64) error {
	if errors.Is(err, store.ErrNoClaim) {
		return serr.E(serr.InvalidInput,
			"claim %d is not active — it expired or was already released; acquire it again if you still need it", id)
	}
	return err
}
