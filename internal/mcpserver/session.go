// Package mcpserver exposes stigmergy over MCP: the memory, claim and mailbox
// tools every agent shares, behind a small state machine that makes an agent
// declare who and where it is before it can change anything.
package mcpserver

import (
	"os"
	"sync"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/hosts"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// State is the connection lifecycle: UNOPENED --context_open--> OPENED
// --root_register--> REGISTERED.
type State int

const (
	Unopened State = iota
	Opened
	Registered
)

// Session is the per-connection state. One host session runs one `stigmergy
// mcp` process, but handlers can still be called concurrently, so everything
// goes through the mutex.
type Session struct {
	mu      sync.Mutex
	state   State
	repo    *gitx.Repo
	project *store.DB
	global  *store.DB
	root    *store.Root

	globalPath string
}

// NewSession builds an unopened session bound to a global DB path.
func NewSession(globalPath string) *Session {
	return &Session{globalPath: globalPath}
}

// bootstrapFromEnv opens the project and registers the root from the session a
// host left in the environment, so the agent can claim and remember without the
// context_open/root_register handshake it routinely skips.
//
// It is the whole reason a Claude Code agent in a busy repo no longer has to be
// nagged into registering: the host exports its session id, this server reads it,
// and the claim guard — which keys "my own claims" on that same id — recognizes
// the root without the agent lifting a finger. The session label is the host's
// real id, not a synthetic one, precisely so the two agree.
//
// Best-effort and silent: any missing piece (no such host, not a git repo,
// stigmergy not adopted here, a database that will not open) leaves the session
// Unopened, and the explicit handshake still works. A failure here must never
// stop the server from serving.
func (s *Session) bootstrapFromEnv() {
	host, sessionID, projectDir, ok := hosts.SelfRegistration(os.Getenv)
	if !ok || projectDir == "" {
		return
	}
	repo, err := gitx.Resolve(projectDir)
	if err != nil {
		return
	}
	if _, err := os.Stat(store.ProjectDBPath(repo.CommonDir)); err != nil {
		return // stigmergy is not enabled here; do not create a database.
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state >= Opened {
		return // The agent already opened it explicitly; do not fight that.
	}

	project, err := store.OpenProject(repo.CommonDir)
	if err != nil {
		return
	}
	global, err := store.OpenGlobal(s.globalPath)
	if err != nil {
		project.Close()
		return
	}
	// Model is left empty: the environment carries the harness, never the model,
	// and the agent can fill it later with root_register if it wants a roster
	// line. Everything else resumes on (agent_kind, worktree, session_label), so a
	// server restarted on /clear reconnects to the same root and its claims.
	root, _, err := project.RegisterRoot(store.Registration{
		RootID:       ids.NewRootID(),
		AgentKind:    host.Kind,
		Worktree:     repo.WorktreeRoot,
		SessionLabel: sessionID,
	})
	if err != nil {
		project.Close()
		global.Close()
		return
	}

	if s.project != nil {
		s.project.Close()
	}
	if s.global != nil {
		s.global.Close()
	}
	s.repo, s.project, s.global = repo, project, global
	s.root, s.state = root, Registered
	_, _ = project.ReapStaleRoots()
}

// Close releases both databases.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project != nil {
		s.project.Close()
		s.project = nil
	}
	if s.global != nil {
		s.global.Close()
		s.global = nil
	}
	s.state = Unopened
	return nil
}

// EndRoot marks the root ended on a clean shutdown, so its claims free up
// immediately instead of waiting out the TTL. Best-effort by design: if the
// host kills the process, the TTL is the real safety net.
func (s *Session) EndRoot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Registered && s.project != nil && s.root != nil {
		_ = s.project.DeregisterRoot(s.root.RootID)
		s.root = nil
		s.state = Opened
	}
}

// requireOpened gates read tools. It returns an error that names the call the
// agent skipped, because the agent has to fix this without a human.
func (s *Session) requireOpened() error {
	if s.state < Opened {
		return serr.E(serr.WrongState,
			"no project is open — call context_open with the absolute path of your working directory first")
	}
	return nil
}

// requireRegistered gates every mutation and the mailbox: an agent that has not
// said who it is cannot own a claim or be answered.
func (s *Session) requireRegistered() error {
	if err := s.requireOpened(); err != nil {
		return err
	}
	if s.state < Registered {
		return serr.E(serr.WrongState,
			"this session has no root — call root_register (with your host session id as session_label) before changing anything")
	}
	return nil
}

// touch refreshes the root's liveness. Every registered call is a heartbeat, so
// an agent that is actively working never loses its claims to the TTL.
func (s *Session) touch() {
	if s.state == Registered && s.root != nil {
		_ = s.project.Heartbeat(s.root.RootID)
	}
}

// agentKind is the registered root's kind, or "" before registration.
func (s *Session) agentKind() string {
	if s.root == nil {
		return ""
	}
	return s.root.AgentKind
}

// actor is the registered root's id, or "" before registration.
func (s *Session) actor() string {
	if s.root == nil {
		return ""
	}
	return s.root.RootID
}

// scopeDB maps a scope name onto its database.
func (s *Session) scopeDB(scope string) (*store.DB, error) {
	switch store.Kind(scope) {
	case store.Project:
		return s.project, nil
	case store.Global:
		return s.global, nil
	default:
		return nil, serr.E(serr.InvalidInput, "scope %q is invalid: must be \"project\" or \"global\"", scope)
	}
}
