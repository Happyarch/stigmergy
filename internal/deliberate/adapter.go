package deliberate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Adapter turns a payload into text. That is its whole job.
//
// Adapters know nothing about rounds, roles, rotation or verdicts — those are
// the driver's. What they own is the part that cannot be reasoned about and had
// to be discovered by running each CLI: how to say "do not ask", where the
// session id comes from, and which directory the host writes to.
type Adapter interface {
	// Kind is the agent_kind, matching internal/hosts.
	Kind() string

	// StateDirs are absolute paths the host must be able to write for its
	// sessions to survive. Bound rw inside the sandbox; everything else is
	// read-only.
	StateDirs() []string

	// Turn runs one turn. session is empty on the first turn of a slot and
	// thereafter whatever Turn last returned. It returns the agent's text and
	// the session to use next time.
	//
	// prompt is deliberately short and nearly constant: the turn's real brief is
	// a file bound into the sandbox at SandboxPayloadPath, and prompt is the
	// sentence that points at it. Adapters decide how to DELIVER a payload;
	// nothing in an adapter decides what is in one (docs/deliberation.md §5).
	//
	// A returned session of "" means this host could not establish one; the
	// driver falls back to a full payload every turn, which works because the
	// payload is self-contained by design.
	Turn(ctx context.Context, a *Agent, prompt string) (out, session string, err error)
}

// Handoff is the whole argv prompt. Everything else is in the bound file.
//
// One mechanism for all four hosts, rather than stdin for the three that support
// it and a file for the one that does not. antigravity's --print/--prompt are
// Go-style VALUED flags — the prompt IS the flag's value, there is no positional
// slot and no documented stdin path — so a file was required there regardless,
// and two delivery mechanisms across four adapters is strictly worse than one.
//
// Always used, never size-switched. A "spill to a file only when it is large"
// branch would be exercised only on long runs: exactly the runs that are already
// in trouble, and the ones you can least afford to lose to an untested path.
const Handoff = "Read " + SandboxPayloadPath + " and follow it exactly.\n\n" +
	"That file is your complete brief for this turn: your role, what is being specified, " +
	"the current specification, and any questions or critique you are meant to act on. " +
	"It is the whole instruction — there is nothing else coming, and nothing to ask for."

// Agent is one slot in the rotation: a host, a model, and the session that
// belongs to it for the life of the run.
//
// The session is per *slot*, never per role. An agent keeps its own memory as it
// rotates through Planner, Guide and Adversary — that is its own reasoning, and
// remembering it is a feature. What it must never get is another agent's
// history, which is what the payload contract prevents.
//
// Every agent shares one Workdir — the real repository — because isolation comes
// from the per-process overlay, not from separate directories. Two agents at the
// same path cannot see each other's writes: each process gets its own tmpfs upper
// layer. So the payload contract holds by construction rather than by keeping
// them in different rooms.
type Agent struct {
	Slot   int
	Kind   string
	Model  string
	Effort string
	// Workdir is where this agent starts: the project's primary repository. It
	// is NOT the extent of what the agent can reach — see Confine.Repos, which
	// overlays every repository of the project.
	Workdir string
	Session string
	// PayloadPath is the host-side file holding this turn's brief. The driver
	// writes it and sets this before each turn; the sandbox binds it in at
	// SandboxPayloadPath. Per-turn rather than per-slot, unlike everything else
	// here — it is state the driver hands down, not state the agent accumulates.
	PayloadPath string
	// Confine carries the repositories to overlay and the run directory to hide.
	// Set once by the driver; StateDirs, Chdir and PayloadPath are filled in per
	// invocation from the adapter and the fields above.
	Confine Sandbox
}

// WorkDirs are every repository the agent may write in, outermost first.
//
// Hosts that take a repeatable directory flag get all of them — verified live:
// agy's --add-dir is documented "(repeatable)" and claude's takes a variadic
// list. Hosts with a singular flag (opencode's --dir) get Workdir and reach the
// rest through the sandbox, which does not care what the host was told.
func (a *Agent) WorkDirs() []string {
	if len(a.Confine.Repos) == 0 {
		return []string{a.Workdir}
	}
	return a.Confine.Repos
}

func (a *Agent) String() string { return fmt.Sprintf("slot %d (%s/%s)", a.Slot, a.Kind, a.Model) }

// NewAdapter returns the adapter for a host.
func NewAdapter(kind string) (Adapter, error) {
	switch kind {
	case "codex":
		return codexAdapter{}, nil
	case "claude-code":
		return claudeAdapter{}, nil
	case "antigravity":
		return antigravityAdapter{}, nil
	case "opencode":
		return opencodeAdapter{}, nil
	default:
		return nil, fmt.Errorf("no deliberate adapter for host %q (have: codex, claude-code, antigravity, opencode)", kind)
	}
}

func home(rel ...string) string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{h}, rel...)...)
}
