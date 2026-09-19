package deliberate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// opencodeAdapter drives `opencode run` — the cheap seat in the rotation.
//
// Established against opencode v2.0.10 (flags re-checked on the 2.0.8 binary;
// the run flags did not change between them):
//
//  1. The prompt is a POSITIONAL, not a flag: `opencode run <message>`. Every
//     option precedes it, and there is no `--` prompt convention like codex's; a
//     deliberate prompt never begins with a dash, so a bare trailing positional
//     is unambiguous.
//  2. --standalone runs a private server instead of the background service. That
//     is load-bearing, not tidiness, for two reasons. The stigmergy enforcement
//     plugin stands down inside deliberation (see 3), and it can only see the
//     driver's opt-out when it loads in the worker's own process — a shared
//     service would run the plugin in its own environment, outside the sandbox,
//     where the variable never arrives. It also keeps an ephemeral worker from
//     sharing server state with the user's interactive sessions.
//  3. There is no --pure anymore, and nothing else that disables the project's
//     plugins — so the plugin stands itself down. When STIGMERGY_UNGUARDED is
//     set (bwrap sets it for every worker; only this plugin reads it), setup
//     registers nothing: no claim blocking against the worker's own writes,
//     no registration text in its prompt. The V1 --pure did both of these by
//     not loading the plugin at all; the effect is the same. Neither is the
//     boundary — bwrap is (§6.4); this only stops the worker blocking on
//     itself. --auto then auto-approves opencode's native permission prompts.
//  4. Reasoning effort is a #variant suffix on the model
//     (provider/model#variant), NOT a separate flag: V1's --variant is gone.
//     Empty effort means no suffix and the model's default.
//  5. There is no --dir either. The workdir comes from the process working
//     directory, which runWrapped already sets to the agent's workdir both
//     outside bwrap (Cmd.Dir) and inside it (--chdir) — so there is nothing to
//     pass. A singular --dir was never the extent of what the worker could
//     reach anyway; the sandbox is.
//
// Sessionless by design. opencode prints no resumable session id without parsing
// its JSON event stream, and the payload is self-contained, so this host returns
// "" every turn and the driver re-sends the full payload — the degraded mode the
// [Adapter] contract explicitly supports. The only cost is a context rebuild per
// turn; the correctness of the rotation does not depend on it.
type opencodeAdapter struct{}

func (opencodeAdapter) Kind() string { return "opencode" }

// StateDirs is opencode's data directory — its session store and logs. Bound rw
// so a turn's own writes there succeed against the overlay, even though the
// driver never resumes from them.
func (opencodeAdapter) StateDirs() []string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return []string{filepath.Join(d, "opencode")}
	}
	return []string{home(".local", "share", "opencode")}
}

func (o opencodeAdapter) Turn(ctx context.Context, a *Agent, prompt string) (string, string, error) {
	model := a.Model
	if a.Effort != "" && !strings.Contains(model, "#") {
		model += "#" + a.Effort
	}
	args := []string{"opencode", "run", "--standalone", "--auto", "--model", model, prompt}

	out, err := runWrapped(ctx, a, o.StateDirs(), args)
	if err != nil {
		return "", a.Session, err
	}
	return strings.TrimSpace(out), "", nil
}
