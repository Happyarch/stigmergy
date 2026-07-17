package deliberate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// codexAdapter drives `codex exec`.
//
// Every flag here was established by running it against codex 0.144.5, and two
// of them are the opposite of what the documentation implies.
type codexAdapter struct{}

func (codexAdapter) Kind() string { return "codex" }

func (codexAdapter) StateDirs() []string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return []string{h}
	}
	return []string{home(".codex")}
}

func (c codexAdapter) Turn(ctx context.Context, a *Agent, prompt string) (string, string, error) {
	args := []string{"codex", "exec"}
	if a.Session != "" {
		args = append(args, "resume", a.Session)
	}

	// -m goes on EVERY invocation, resume included, for three independent
	// reasons — each of which fails silently:
	//
	//  1. `codex exec resume` does not inherit the session's model. It falls
	//     back to config.toml's default and only warns: "This session was
	//     recorded with model X but is resuming with Y". Then it proceeds.
	//  2. codex's interactive TUI *rewrites* config.toml. A user changing model
	//     in another terminal moves the default under a running driver.
	//  3. An agent that changes model mid-run is not the mind that took the
	//     role. The guarantee this whole pipeline sells is three stable,
	//     different minds; a silent model swap voids it.
	//
	// None of this is in the manual: its "Resume a non-interactive session"
	// section never mentions the model.
	args = append(args, "-m", a.Model)
	if a.Effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", a.Effort))
	}

	args = append(args,
		// Both of these go through -c rather than through their flags, and for
		// the same reason: the flags do not exist on both subcommands.
		//
		//   --ask-for-approval  is top-level only. `codex exec` rejects it.
		//   --sandbox           is on `codex exec` but NOT on `codex exec resume`.
		//
		// Either one passed to the wrong subcommand is a hard argument error, so
		// a driver that resumes would die on its second turn — which is exactly
		// what this one did, once, before this comment existed.
		//
		// `-c` is accepted by both, and both keys validate their values
		// (approval_policy: untrusted|on-failure|on-request|granular|never;
		// sandbox_mode: read-only|workspace-write|danger-full-access), so a typo
		// here fails loudly. That is not true of an unknown -c *key*, which codex
		// accepts in silence — hence only ever using keys checked against a live
		// process.
		//
		// Belt-and-braces regardless: bwrap is the real boundary, and exec
		// already defaults to read-only. A default is someone else's to change.
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `approval_policy="never"`,
		// The worker must not see stigmergy's tools: it is not a root and must
		// not register, claim, or write memory.
		"-c", "mcp_servers.stigmergy.enabled=false",
		"--json",
		"--skip-git-repo-check",
	)
	// NOT --ephemeral. explore passes it deliberately to prevent session state;
	// we need exactly the opposite from the same binary.
	args = append(args, "--", prompt)

	stdout, err := runWrapped(ctx, a, c.StateDirs(), args)
	if err != nil {
		return "", a.Session, err
	}
	text, session := parseCodexJSON(stdout)
	if session == "" {
		session = a.Session
	}
	return text, session, nil
}

// parseCodexJSON reads the JSONL event stream.
//
// The session id arrives in the very first event — {"type":"thread.started",
// "thread_id":"…"} — before the turn starts, and resume re-emits the same id, so
// a driver can assert it reconnected to the thread it meant to rather than
// assume.
func parseCodexJSON(out string) (text, session string) {
	var last string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.ThreadID != "" {
			session = ev.ThreadID
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" {
			last = ev.Item.Text
		}
	}
	return last, session
}
