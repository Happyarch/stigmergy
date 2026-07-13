package mcpserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// ContextOpenInput opens a project.
type ContextOpenInput struct {
	ProjectRoot string `json:"project_root" jsonschema:"absolute path to your working directory inside the git repository"`
}

// ContextOpenOutput reports what was opened.
type ContextOpenOutput struct {
	WorktreeRoot     string `json:"worktree_root"`
	ProjectCommonDir string `json:"project_common_dir"`
	SchemaVersion    int    `json:"schema_version"`
	Reopened         bool   `json:"reopened"`
}

func (s *Session) contextOpen(_ context.Context, _ *mcp.CallToolRequest, in ContextOpenInput) (*mcp.CallToolResult, ContextOpenOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !filepath.IsAbs(in.ProjectRoot) {
		return nil, ContextOpenOutput{}, toolError(serr.E(serr.InvalidInput,
			"project_root must be an absolute path, got %q", in.ProjectRoot))
	}
	repo, err := gitx.Resolve(in.ProjectRoot)
	if err != nil {
		if errors.Is(err, gitx.ErrNotARepo) {
			return nil, ContextOpenOutput{}, toolError(serr.E(serr.NotARepo,
				"%s is not inside a git repository — stigmergy coordinates per repository", in.ProjectRoot))
		}
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not resolve the git repository for %s", in.ProjectRoot))
	}

	// Idempotent for the same repo: re-opening keeps the registered root, so a
	// re-issued context_open cannot silently strand an agent's claims.
	if s.state >= Opened && s.repo.CommonDir == repo.CommonDir {
		v, err := s.project.SchemaVersion()
		if err != nil {
			return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the schema version"))
		}
		return nil, ContextOpenOutput{
			WorktreeRoot: s.repo.WorktreeRoot, ProjectCommonDir: s.repo.CommonDir,
			SchemaVersion: v, Reopened: true,
		}, nil
	}

	project, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not open the project database"))
	}
	global, err := store.OpenGlobal(s.globalPath)
	if err != nil {
		project.Close()
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not open the global database"))
	}
	v, err := project.SchemaVersion()
	if err != nil {
		project.Close()
		global.Close()
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the schema version"))
	}

	if s.project != nil {
		s.project.Close()
	}
	if s.global != nil {
		s.global.Close()
	}
	s.repo, s.project, s.global = repo, project, global
	s.state, s.root = Opened, nil

	// Opportunistic housekeeping, off the hot path: nothing else ever ends the
	// roots of agents that died without deregistering.
	_, _ = project.ReapStaleRoots()

	return nil, ContextOpenOutput{
		WorktreeRoot: repo.WorktreeRoot, ProjectCommonDir: repo.CommonDir, SchemaVersion: v,
	}, nil
}

// RootRegisterInput registers (or resumes) a root session.
type RootRegisterInput struct {
	AgentKind    string `json:"agent_kind" jsonschema:"which host you are: claude-code or codex"`
	Worktree     string `json:"worktree" jsonschema:"absolute path of the worktree you are working in"`
	Branch       string `json:"branch,omitempty" jsonschema:"branch you are on, if known"`
	SessionLabel string `json:"session_label,omitempty" jsonschema:"your host session id; supply it so your own edits are not blocked by your own claims"`
}

// RootRegisterOutput carries the root identity other agents address you by.
type RootRegisterOutput struct {
	Root    *store.Root `json:"root"`
	Resumed bool        `json:"resumed"`
}

func (s *Session) rootRegister(_ context.Context, _ *mcp.CallToolRequest, in RootRegisterInput) (*mcp.CallToolResult, RootRegisterOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, RootRegisterOutput{}, toolError(err)
	}
	// No defaulting for an empty worktree: it is a required field, and a root
	// resumes on (agent_kind, worktree, session_label). Quietly substituting a
	// worktree the caller never named would register it under a key it does not
	// know, so the next session's resume would miss and strand its claims.
	// store.RegisterRoot rejects it and says what is wrong.
	root, resumed, err := s.project.RegisterRoot(store.Registration{
		RootID:       ids.NewRootID(),
		AgentKind:    in.AgentKind,
		Worktree:     in.Worktree,
		Branch:       in.Branch,
		SessionLabel: in.SessionLabel,
	})
	if err != nil {
		return nil, RootRegisterOutput{}, toolError(err)
	}
	s.root, s.state = root, Registered
	return nil, RootRegisterOutput{Root: root, Resumed: resumed}, nil
}

// RootHeartbeatOutput confirms liveness was refreshed.
type RootHeartbeatOutput struct {
	RootID     string `json:"root_id"`
	LastSeenAt string `json:"last_seen_at"`
}

func (s *Session) rootHeartbeat(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RootHeartbeatOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, RootHeartbeatOutput{}, toolError(err)
	}
	if err := s.project.Heartbeat(s.root.RootID); err != nil {
		return nil, RootHeartbeatOutput{}, toolError(rootErr(err))
	}
	return nil, RootHeartbeatOutput{RootID: s.root.RootID, LastSeenAt: store.Now()}, nil
}

// RootDeregisterOutput confirms the root ended.
type RootDeregisterOutput struct {
	RootID string `json:"root_id"`
	Ended  bool   `json:"ended"`
}

func (s *Session) rootDeregister(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RootDeregisterOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, RootDeregisterOutput{}, toolError(err)
	}
	id := s.root.RootID
	if err := s.project.DeregisterRoot(id); err != nil {
		return nil, RootDeregisterOutput{}, toolError(rootErr(err))
	}
	s.root, s.state = nil, Opened
	return nil, RootDeregisterOutput{RootID: id, Ended: true}, nil
}

// rootErr turns a vanished root into advice the agent can act on: its session
// outlived its registration (TTL, or another process ended it), so it must
// register again rather than keep calling with a dead id.
func rootErr(err error) error {
	if errors.Is(err, store.ErrNoRoot) {
		return serr.E(serr.WrongState,
			"your root is no longer active — it expired or was ended; call root_register again (your previous claims have been released)")
	}
	return err
}
