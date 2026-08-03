package mcpserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// ContextOpenInput opens a project.
type ContextOpenInput struct {
	ProjectRoot string `json:"project_root" jsonschema:"absolute path to your working directory inside the git repository"`
}

// RepoInfo is one repository of the project, as reported to an agent.
type RepoInfo struct {
	Repo     string `json:"repo"`
	Worktree string `json:"worktree"`
	Self     bool   `json:"self,omitempty"`
}

// ContextOpenOutput reports what was opened.
type ContextOpenOutput struct {
	// ProjectID is set only for a project spanning several repositories.
	ProjectID string `json:"project_id,omitempty"`
	// Repos is the roster. It is returned here rather than from a tool of its
	// own because context_open is the one call every agent already makes, and a
	// separate tool would be one more thing to skip — an agent that does not
	// know the project is wider than its checkout writes claims that mean the
	// wrong repository.
	Repos            []RepoInfo `json:"repos,omitempty"`
	WorktreeRoot     string     `json:"worktree_root"`
	ProjectCommonDir string     `json:"project_common_dir"`
	SchemaVersion    int        `json:"schema_version"`
	Reopened         bool       `json:"reopened"`
}

// roster renders the members for an agent, marking the one it opened from.
func roster(p *project.Project) []RepoInfo {
	if p == nil || len(p.Members) < 2 {
		// A single-repository project has nothing to disambiguate, and saying so
		// would invite an agent to start writing "app:src/x.go" where a bare path
		// is correct and always has been.
		return nil
	}
	out := make([]RepoInfo, 0, len(p.Members))
	for _, m := range p.Members {
		out = append(out, RepoInfo{Repo: m.ID, Worktree: m.WorktreeRoot, Self: m.ID == p.SelfID})
	}
	return out
}

func (s *Session) contextOpen(_ context.Context, _ *mcp.CallToolRequest, in ContextOpenInput) (*mcp.CallToolResult, ContextOpenOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !filepath.IsAbs(in.ProjectRoot) {
		return nil, ContextOpenOutput{}, toolError(serr.E(serr.InvalidInput,
			"project_root must be an absolute path, got %q", in.ProjectRoot))
	}
	proj, err := project.ResolveWithFallback(in.ProjectRoot)
	if err != nil {
		if errors.Is(err, project.ErrNotARepo) {
			return nil, ContextOpenOutput{}, toolError(serr.E(serr.NotARepo,
				"%s is not inside a git repository stigmergy is enabled in", in.ProjectRoot))
		}
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not resolve the project for %s", in.ProjectRoot))
	}

	// Idempotent for the same PROJECT, not the same repository.
	//
	// That distinction is a bug fix as much as a feature. Keying on the git
	// common dir meant an agent re-opening from a sibling repository of the same
	// project took the close-and-swap path below — dropping s.root and leaving
	// its claims live but unreachable until they timed out, for no reason at all,
	// since both repositories share one database.
	if s.state >= Opened && s.project != nil && s.project.Path == proj.DBPath {
		v, err := s.project.SchemaVersion()
		if err != nil {
			return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the schema version"))
		}
		// The freshly resolved project replaces the old one rather than merely
		// refreshing its roster. Same database, but the resolution is what carries
		// the session's own identity: an agent re-opening from a sibling repository
		// means "I am here now", and keeping the previous SelfID would resolve its
		// bare scope paths against the repository it left — silently claiming the
		// wrong file. Loading also picks up a repository that joined since.
		if err := proj.Load(s.project); err != nil {
			return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the project's repositories"))
		}
		s.proj = proj
		return nil, ContextOpenOutput{
			ProjectID: s.proj.ID, Repos: roster(s.proj),
			WorktreeRoot: s.worktree(), ProjectCommonDir: s.proj.Repo.CommonDir,
			SchemaVersion: v, Reopened: true,
		}, nil
	}

	db, err := store.OpenProjectAt(proj.DBPath)
	if err != nil {
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not open the project database"))
	}
	global, err := store.OpenGlobal(s.globalPath)
	if err != nil {
		db.Close()
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not open the global database"))
	}
	v, err := db.SchemaVersion()
	if err != nil {
		db.Close()
		global.Close()
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the schema version"))
	}
	if err := proj.Load(db); err != nil {
		db.Close()
		global.Close()
		return nil, ContextOpenOutput{}, toolError(serr.Internalf(err, "could not read the project's repositories"))
	}

	if s.project != nil {
		s.project.Close()
	}
	if s.global != nil {
		s.global.Close()
	}
	s.proj, s.project, s.global = proj, db, global
	s.state, s.root = Opened, nil

	// Opportunistic housekeeping, off the hot path: nothing else ever ends the
	// roots of agents that died without deregistering.
	_, _ = db.ReapStaleRoots()
	// An agent opening this project is proof it is in use, so record it for
	// `doctor --all`. Best-effort, and never on the hook path — see
	// store.RememberProject.
	_ = global.RememberProject(db.Path, db.Label(proj.Repo.WorktreeRoot))

	return nil, ContextOpenOutput{
		ProjectID: proj.ID, Repos: roster(proj),
		WorktreeRoot: proj.Repo.WorktreeRoot, ProjectCommonDir: proj.Repo.CommonDir,
		SchemaVersion: v,
	}, nil
}

// RootRegisterInput registers (or resumes) a root session.
//
// Model is asked rather than detected because there is nothing to detect: no
// hook payload and no MCP handshake carries it, and agent_kind names only the
// harness. The answer is unverified and nothing keys off it, which is exactly
// why asking is fine — the cost of a wrong one is a wrong line in the roster.
type RootRegisterInput struct {
	// The host list is spelled out because a jsonschema tag is a compile-time
	// constant and cannot be built from the hosts registry. It is the one place
	// left that repeats the set, so a test asserts it still matches — see
	// TestTheAgentKindSchemaNamesEveryHost.
	AgentKind    string `json:"agent_kind" jsonschema:"which host you are: claude-code, codex, antigravity, or opencode"`
	Worktree     string `json:"worktree" jsonschema:"absolute path of the worktree you are working in"`
	Branch       string `json:"branch,omitempty" jsonschema:"branch you are on, if known"`
	SessionLabel string `json:"session_label,omitempty" jsonschema:"your host session id; supply it so your own edits are not blocked by your own claims"`
	Model        string `json:"model,omitempty" jsonschema:"which model you are: your own model id, such as claude-opus-4-8 or gemini-3-pro. agent_kind is the harness; this is you. Say what you actually are - if you are unsure of your exact id, give your best answer rather than omitting it, and never copy the example"`
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
	// No defaulting for an empty worktree: it is a required field. It is not part
	// of the resume key — see RegisterRoot — but it is where this agent says it is
	// working, which is what the roster and every conflict message report, and a
	// directory the caller never named would be misleading in both.
	// store.RegisterRoot rejects it and says what is wrong.
	root, resumed, err := s.project.RegisterRoot(store.Registration{
		RootID:       ids.NewRootID(),
		AgentKind:    in.AgentKind,
		Worktree:     in.Worktree,
		Branch:       in.Branch,
		SessionLabel: in.SessionLabel,
		Model:        in.Model,
	})
	if err != nil {
		return nil, RootRegisterOutput{}, toolError(err)
	}
	s.root, s.state = root, Registered
	return nil, RootRegisterOutput{Root: root, Resumed: resumed}, nil
}

// RootListActiveOutput is the roster: who is working in this repository right
// now, and what each of them holds.
type RootListActiveOutput struct {
	Roots []ActiveRoot `json:"roots"`
}

// ActiveRoot is another agent, described the way you need it described in order
// to decide whether to write to it.
type ActiveRoot struct {
	RootID    string `json:"root_id"`
	AgentKind string `json:"agent_kind"`
	// Model is what this agent said it was, when it said anything. This is the
	// roster an agent reads before deciding who to write to, so it is the place
	// the answer is worth the most: agent_kind tells you the harness, and two
	// claude-code roots can be very different correspondents.
	Model    string   `json:"model,omitempty"`
	Worktree string   `json:"worktree"`
	Branch   string   `json:"branch,omitempty"`
	Liveness string   `json:"liveness"`
	Holds    []string `json:"holds"`
	IsYou    bool     `json:"is_you"`
}

// rootListActive answers the question an agent has to get right before it can
// negotiate at all: who is actually here?
//
// Nothing used to answer it. An agent that wanted to write to the owner of a
// claim had to have kept the root id from a conflict message — across compaction,
// across its own summarizing, across whatever else it had been doing since — and
// a root id remembered wrongly is not an error, it is an address. Mail sent to a
// root that has since died was accepted and never read, and the sender waited on
// an answer that could not come. Meanwhile the agent that was actually holding
// the file, and could have handed it over in one exchange, was never asked.
//
// So: a roster, freshly read, that an agent can consult instead of remembering.
func (s *Session) rootListActive(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RootListActiveOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, RootListActiveOutput{}, toolError(err)
	}
	// Resolved before the roster is built, because "which of these is me" is the
	// one line an agent acts on, and in a session running several agents the
	// answer is no longer "the session".
	me := s.actingRoot(req)
	roots, err := s.project.ActiveRoots()
	if err != nil {
		return nil, RootListActiveOutput{}, toolError(err)
	}
	claims, err := s.project.ActiveClaims("")
	if err != nil {
		return nil, RootListActiveOutput{}, toolError(err)
	}
	held := map[string][]string{}
	for _, c := range claims {
		// Qualified, not bare: two members of a project can both contain
		// "src/api.go", and a roster that says only "src/api" leaves the reader
		// unable to tell whether the path in front of it is the one held.
		scope := c.Qualified()
		if c.Recursive {
			scope += "/**"
		}
		held[c.RootID] = append(held[c.RootID], scope)
	}

	out := RootListActiveOutput{Roots: []ActiveRoot{}}
	for _, r := range roots {
		holds := held[r.RootID]
		if holds == nil {
			holds = []string{}
		}
		out.Roots = append(out.Roots, ActiveRoot{
			RootID: r.RootID, AgentKind: r.AgentKind, Model: r.Model,
			Worktree: r.Worktree, Branch: r.Branch,
			Liveness: r.Liveness(), Holds: holds, IsYou: r.RootID == rootID(me),
		})
	}
	s.heartbeat(me)
	return nil, out, nil
}

// RootHeartbeatOutput confirms liveness was refreshed.
type RootHeartbeatOutput struct {
	RootID     string `json:"root_id"`
	LastSeenAt string `json:"last_seen_at"`
}

func (s *Session) rootHeartbeat(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RootHeartbeatOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, RootHeartbeatOutput{}, toolError(err)
	}
	// An agent keeping itself alive keeps ITSELF alive. Heartbeating the session
	// instead would be the worst of both: the session lives on the strength of an
	// agent's work, and the agent's own claims lapse while it is still working.
	me := s.actingRoot(req)
	if err := s.project.Heartbeat(me.RootID); err != nil {
		return nil, RootHeartbeatOutput{}, toolError(rootErr(err))
	}
	return nil, RootHeartbeatOutput{RootID: me.RootID, LastSeenAt: store.Now()}, nil
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
