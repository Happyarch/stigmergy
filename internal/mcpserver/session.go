// Package mcpserver exposes stigmergy over MCP: the memory, claim and mailbox
// tools every agent shares, behind a small state machine that makes an agent
// declare who and where it is before it can change anything.
package mcpserver

import (
	"sync"

	"github.com/happyarch/stigmergy/internal/gitx"
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
