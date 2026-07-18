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
	// A returned session of "" means this host could not establish one; the
	// driver falls back to a full payload every turn, which works because the
	// payload is self-contained by design.
	Turn(ctx context.Context, a *Agent, prompt string) (out, session string, err error)
}

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
	Slot    int
	Kind    string
	Model   string
	Effort  string
	Workdir string
	Session string
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
