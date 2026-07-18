package deliberate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// opencodeAdapter drives `opencode run` — the cheap seat in the rotation.
//
// Established against opencode v1.17.20:
//
//  1. The prompt is a POSITIONAL, not a flag: `opencode run <message>`. Every
//     option precedes it, and there is no `--` prompt convention like codex's; a
//     deliberate prompt never begins with a dash, so a bare trailing positional
//     is unambiguous.
//  2. --pure disables external plugins, and that is load-bearing, not tidiness:
//     the stigmergy enforcement plugin this repo installs would otherwise apply
//     claim blocking to the *worker's own* writes and stall a Planner that edits
//     while grounding itself. --auto then auto-approves opencode's native
//     permission prompts. Neither is the boundary — bwrap is (§6.4); these only
//     stop the worker blocking on itself.
//  3. Reasoning effort is --variant (provider-specific: high, max, minimal), NOT
//     an --effort flag. Empty effort means omit it and take the model's default.
//  4. The model is `provider/model` (e.g. lmstudio/qwen/qwen3.6-27b); that shape
//     is the caller's to supply in the --agents spec.
//
// Sessionless by design. opencode prints no resumable session id without parsing
// its JSON event stream, and the payload is self-contained, so this host returns
// "" every turn and the driver re-sends the full payload — the degraded mode the
// [Adapter] contract explicitly supports. The only cost is a context rebuild per
// turn; the correctness of the rotation does not depend on it.
type opencodeAdapter struct{}

func (opencodeAdapter) Kind() string { return "opencode" }

// StateDirs is opencode's data directory — its session store and logs. Bound rw
// so a turn's own writes there succeed against the overlay, even though we never
// resume from them.
func (opencodeAdapter) StateDirs() []string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return []string{filepath.Join(d, "opencode")}
	}
	return []string{home(".local", "share", "opencode")}
}

func (o opencodeAdapter) Turn(ctx context.Context, a *Agent, prompt string) (string, string, error) {
	args := []string{"opencode", "run", "--pure", "--auto", "--model", a.Model}
	if a.Effort != "" {
		args = append(args, "--variant", a.Effort)
	}
	// --dir is redundant with bwrap's --chdir but harmless, and it is the
	// documented way opencode is told which directory it works in.
	args = append(args, "--dir", a.Workdir)
	// The prompt is the trailing positional message.
	args = append(args, prompt)

	out, err := runWrapped(ctx, a, o.StateDirs(), args)
	if err != nil {
		return "", a.Session, err
	}
	return strings.TrimSpace(out), "", nil
}
