// Package mcpserver exposes stigmergy over MCP: the memory, claim and mailbox
// tools every agent shares, behind a small state machine that makes an agent
// declare who and where it is before it can change anything.
package mcpserver

import (
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/hosts"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/project"
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
	mu    sync.Mutex
	state State
	// proj is which project is open and which repositories it spans. One
	// project, one database — that is the whole payoff of the shared-DB design,
	// and it is why a session that moves between sibling repositories no longer
	// has to close and reopen anything.
	proj    *project.Project
	project *store.DB
	global  *store.DB
	root    *store.Root

	globalPath string
}

// selfRepo is the member the session opened from, or "" in a single-repository
// project, where naming the only repository there is would be noise.
func (s *Session) selfRepo() string {
	if s.proj == nil {
		return ""
	}
	return s.proj.SelfID
}

// memberIDs is the roster, for parsing and for error messages that have to say
// what the valid answers were.
func (s *Session) memberIDs() []string {
	if s.proj == nil {
		return nil
	}
	return s.proj.MemberIDs()
}

// worktree is the resolved member's worktree root.
func (s *Session) worktree() string {
	if s.proj == nil || s.proj.Repo == nil {
		return ""
	}
	return s.proj.Repo.WorktreeRoot
}

// commonDir is the git common dir of the repository the session opened from.
// Audit detail only — it records where a promoted memory came from.
func (s *Session) commonDir() string {
	if s.proj == nil || s.proj.Repo == nil {
		return ""
	}
	return s.proj.Repo.CommonDir
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
	proj, err := project.Resolve(projectDir)
	if err != nil {
		return // not a repository, or stigmergy is not enabled here.
	}
	if _, err := os.Stat(proj.DBPath); err != nil {
		return // do not create a database from a bootstrap path.
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state >= Opened {
		return // The agent already opened it explicitly; do not fight that.
	}

	db, err := store.OpenProjectAt(proj.DBPath)
	if err != nil {
		return
	}
	if err := proj.Load(db); err != nil {
		db.Close()
		return
	}
	global, err := store.OpenGlobal(s.globalPath)
	if err != nil {
		db.Close()
		return
	}
	// Model is left empty: the environment carries the harness, never the model,
	// and the agent can fill it later with root_register if it wants a roster
	// line. Everything else resumes on (agent_kind, session_label), so a server
	// restarted on /clear reconnects to the same root and its claims.
	root, _, err := db.RegisterRoot(store.Registration{
		RootID:       ids.NewRootID(),
		AgentKind:    host.Kind,
		Worktree:     proj.Repo.WorktreeRoot,
		SessionLabel: sessionID,
	})
	if err != nil {
		db.Close()
		global.Close()
		return
	}

	if s.project != nil {
		s.project.Close()
	}
	if s.global != nil {
		s.global.Close()
	}
	s.proj, s.project, s.global = proj, db, global
	s.root, s.state = root, Registered
	_, _ = db.ReapStaleRoots()
	_ = global.RememberProject(db.Path, db.Label(proj.Repo.WorktreeRoot))
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

// heartbeat refreshes the acting root rather than the session's. An agent that
// is working must keep its OWN claims alive; refreshing the session on its
// behalf would keep the wrong root alive and let the working agent's claims
// lapse underneath it.
// The session root is refreshed too when the acting root is one of its agents:
// the two live in one process, so an agent's call is proof the session is alive,
// and the session is usually the one holding the claims that must survive while
// it waits for its agents to finish.
func (s *Session) heartbeat(root *store.Root) {
	if s.state != Registered || root == nil {
		return
	}
	_ = s.project.Heartbeat(root.RootID)
	if s.root != nil && s.root.RootID != root.RootID {
		_ = s.project.Heartbeat(s.root.RootID)
	}
}

// rootID is the id of a root that may not exist yet — an unregistered session
// asking a read-only question has no identity, and "" is what the store reads as
// "nothing is your own".
func rootID(root *store.Root) string {
	if root == nil {
		return ""
	}
	return root.RootID
}

// callerToolUseID is where Claude Code puts the id of the tool call being made.
// It is undocumented and was read off the wire (see the caller_tickets
// migration); an unrecognized or absent key simply means no identity, which
// resolves to the session root exactly as every call did before.
const callerToolUseID = "claudecode/toolUseId"

// actingRoot resolves WHICH agent inside the host session made this call.
//
// The problem it solves does not exist on paper and is unavoidable in practice:
// one host session runs several agents, they share this process and this
// connection, and every request therefore arrives looking identical. The server
// has no session of its own to key on and no field in the request that names a
// caller — so for as long as this was unsolved, every agent in a session was the
// same root. They shared claims that were supposed to keep them apart.
//
// What breaks the tie is that the host describes the same call twice: once to
// the PreToolUse hook, which is told agent_id, and once here, in _meta, which is
// told the tool-use id. Both carry that id, so the hook can leave a ticket under
// it and this can pick it up. Neither half is anything the model chooses, which
// is why this is identity rather than an assertion — an agent cannot mint a
// tool-use id, cannot write a ticket, and gains nothing by lying about who it is.
//
// Every failure resolves to the session root: no _meta, no ticket, no hooks
// installed, a host that is not Claude Code. That is the behaviour that existed
// before this function, so a host stigmergy has not learned to read is no worse
// off than it was.
//
// Must be called with s.mu held, and only when the session is registered.
func (s *Session) actingRoot(req *mcp.CallToolRequest) *store.Root {
	if s.state < Registered || s.root == nil || req == nil || req.Params == nil {
		return s.root
	}
	id, _ := req.Params.Meta[callerToolUseID].(string)
	ticket, err := s.project.TakeCallerTicket(id)
	if err != nil || ticket == nil || ticket.SessionLabel == s.root.SessionLabel {
		return s.root
	}

	worktree := ticket.Worktree
	if worktree == "" {
		worktree = s.root.Worktree
	}
	kind := ticket.AgentKind
	if kind == "" {
		kind = s.root.AgentKind
	}
	// Registered on demand, and resumed on every later call by the same agent:
	// the label is stable for the agent's whole life, so this mints one root the
	// first time it calls and finds that same root every time after. Model is left
	// empty for the same reason it is empty on a bootstrapped session root — the
	// host tells us the harness, never the model — and the agent type is already
	// legible in the label.
	root, _, err := s.project.RegisterRoot(store.Registration{
		RootID:       ids.NewRootID(),
		AgentKind:    kind,
		Worktree:     worktree,
		Branch:       s.root.Branch,
		SessionLabel: ticket.SessionLabel,
	})
	if err != nil {
		return s.root
	}
	return root
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
