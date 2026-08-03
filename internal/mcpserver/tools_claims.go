package mcpserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/paths"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// ClaimAcquireInput reserves a path.
type ClaimAcquireInput struct {
	ScopePath  string `json:"scope_path" jsonschema:"path relative to the repository root; \".\" is the whole repo. In a project spanning several repositories, \"repo:path\" names one of them (context_open lists them); a bare path means the repository you opened"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"true to claim a directory and everything under it"`
	Reason     string `json:"reason" jsonschema:"what you are doing; other agents read this when your claim blocks them"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"how long you need it (60-86400; default 1800)"`
}

// ClaimOutput carries a claim.
type ClaimOutput struct {
	Claim *store.Claim `json:"claim"`
}

func (s *Session) claimAcquire(_ context.Context, req *mcp.CallToolRequest, in ClaimAcquireInput) (*mcp.CallToolResult, ClaimOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	// Whoever is actually calling, which in a session running several agents is
	// usually not the session itself. A claim taken under the session's identity
	// would be a claim the taker's neighbours are free to edit straight through.
	me := s.actingRoot(req)
	repoID, scope, err := paths.ParseScope(in.ScopePath, s.memberIDs(), s.selfRepo())
	if err != nil {
		return nil, ClaimOutput{}, toolError(serr.E(serr.InvalidInput, "%s", err.Error()))
	}
	// A colon whose head is not a member parses as a path, silently and
	// plausibly — "sidecar:src/x.go" when the member is "naviamp-sidecar" would
	// claim a file that does not exist while the real one stayed unguarded. So
	// refuse it rather than take a claim nobody benefits from.
	if hint := paths.UnknownRepoHint(in.ScopePath, s.memberIDs()); hint != "" {
		return nil, ClaimOutput{}, toolError(serr.E(serr.InvalidInput, "%s", hint))
	}
	// The repo root is only meaningful as a subtree: a non-recursive claim on
	// "." would name a directory as if it were a file, and match no edit.
	if scope == "." {
		in.Recursive = true
	}

	claim, err := s.project.AcquireClaim(store.ClaimRequest{
		ScopePath: scope, Recursive: in.Recursive, RootID: me.RootID, RepoID: repoID,
		Worktree: me.Worktree, Branch: me.Branch,
		Reason: in.Reason, TTLSeconds: in.TTLSeconds,
	})
	if err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	s.heartbeat(me)
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

func (s *Session) claimCheck(_ context.Context, req *mcp.CallToolRequest, in ClaimCheckInput) (*mcp.CallToolResult, ClaimCheckOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, ClaimCheckOutput{}, toolError(err)
	}
	// Three spellings all have to work here, because an agent asking "is this
	// claimed?" has a path in whatever form it happens to hold: an absolute path
	// anywhere in the project, a path relative to its own repository, or the
	// explicit "repo:path".
	repoID, rel, err := s.resolveCheckPath(in.Path)
	if errors.Is(err, paths.ErrOutsideWorktree) {
		// Claims govern the project's repositories. Outside all of them there is
		// nothing to report and nothing to block.
		return nil, ClaimCheckOutput{Path: in.Path, Claimed: false}, nil
	}
	if err != nil {
		return nil, ClaimCheckOutput{}, toolError(serr.E(serr.InvalidInput, "%s", err.Error()))
	}

	// own is answered for the agent that asked, not for the session it lives in:
	// "you already hold this" and "your neighbour holds this" are opposite
	// instructions, and conflating them is how two agents end up editing one file.
	me := s.actingRoot(req)
	found, err := s.project.ClaimsCovering(repoID, rel, rootID(me))
	if err != nil {
		return nil, ClaimCheckOutput{}, toolError(err)
	}
	s.heartbeat(me)
	return nil, ClaimCheckOutput{
		Path: store.QualifyScope(repoID, rel), Claimed: len(found) > 0, Claims: found,
	}, nil
}

// resolveCheckPath maps whatever an agent passed to claim_check onto a member
// repository and a path inside it.
func (s *Session) resolveCheckPath(p string) (repoID, rel string, err error) {
	// An absolute path decides for itself which repository it is in — the same
	// rule the claim guard uses, and for the same reason: where the agent opened
	// the project says nothing about where a file lives.
	if filepath.IsAbs(p) {
		if m := s.proj.Containing(filepath.Clean(p)); m != nil {
			rel, err := paths.Normalize(m.WorktreeRoot, m.WorktreeRoot, p)
			return m.ID, rel, err
		}
		return "", "", paths.ErrOutsideWorktree
	}

	// Relative: either "repo:path", or a path in the repository the agent opened.
	repoID, scope, err := paths.ParseScope(p, s.memberIDs(), s.selfRepo())
	if err != nil {
		return "", "", err
	}
	root := s.worktree()
	if m := s.proj.Member(repoID); m != nil {
		root = m.WorktreeRoot
	}
	rel, err = paths.Normalize(root, root, scope)
	return repoID, rel, err
}

// ClaimListOutput lists every claim in force.
type ClaimListOutput struct {
	Claims []store.Claim `json:"claims"`
}

func (s *Session) claimListActive(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ClaimListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, ClaimListOutput{}, toolError(err)
	}
	me := s.actingRoot(req)
	list, err := s.project.ActiveClaims(rootID(me))
	if err != nil {
		return nil, ClaimListOutput{}, toolError(err)
	}
	s.heartbeat(me)
	return nil, ClaimListOutput{Claims: list}, nil
}

// ClaimRenewInput extends one of your claims.
type ClaimRenewInput struct {
	ClaimID    int64 `json:"claim_id"`
	TTLSeconds int   `json:"ttl_seconds,omitempty" jsonschema:"new lifetime from now (60-86400; default 1800)"`
}

func (s *Session) claimRenew(_ context.Context, req *mcp.CallToolRequest, in ClaimRenewInput) (*mcp.CallToolResult, ClaimOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimOutput{}, toolError(err)
	}
	me := s.actingRoot(req)
	claim, err := s.project.RenewClaim(in.ClaimID, me.RootID, in.TTLSeconds)
	if err != nil {
		return nil, ClaimOutput{}, toolError(claimErr(err, in.ClaimID))
	}
	s.heartbeat(me)
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

func (s *Session) claimRelease(_ context.Context, req *mcp.CallToolRequest, in ClaimReleaseInput) (*mcp.CallToolResult, ClaimReleaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, ClaimReleaseOutput{}, toolError(err)
	}
	me := s.actingRoot(req)
	if err := s.project.ReleaseClaim(in.ClaimID, me.RootID); err != nil {
		return nil, ClaimReleaseOutput{}, toolError(claimErr(err, in.ClaimID))
	}
	s.heartbeat(me)
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
