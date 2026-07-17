package deliberate

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// antigravityAdapter drives `agy --print`.
//
// agy diverges from the other three in three ways, all silent, all found by
// running it:
//
//  1. --print TAKES THE PROMPT AS ITS VALUE. It is not a boolean like claude's
//     -p (the tell is that --prompt is documented as an alias for it). So
//     `agy -p --sandbox … "PROMPT"` passes the *string* "--sandbox" as the
//     prompt and silently drops the real one; the agent then answers a question
//     nobody asked and nothing errors.
//  2. cwd is ignored. Relative paths land in ~/.gemini/antigravity-cli/scratch/,
//     not the working directory. The workspace must be handed over with
//     --add-dir — which is a workspace *declaration*, not a boundary.
//  3. There is no --effort. Effort is baked into the model name, e.g.
//     "Gemini 3.5 Flash (Low)". `agy models` is the authority.
//
// agy also has no filesystem boundary whatsoever: --dangerously-skip-permissions
// auto-approves everything and it will write outside its workspace on request.
// --sandbox is not the answer either — it is a real sandbox, but it relocates
// the agent into agy's own scratch dir rather than confining it where you put
// it. This host is usable only because bwrap holds the line (§6.4).
type antigravityAdapter struct{}

func (antigravityAdapter) Kind() string { return "antigravity" }

func (antigravityAdapter) StateDirs() []string { return []string{home(".gemini")} }

func conversationsDir() string { return home(".gemini", "antigravity-cli", "conversations") }

func (g antigravityAdapter) Turn(ctx context.Context, a *Agent, prompt string) (string, string, error) {
	// Snapshot before the turn: agy prints no session id, so the only way to
	// learn it is to see which conversation DB appears. Diffing beats "newest
	// file" because it cannot lose a race with a human running agy meanwhile.
	before := conversationIDs()

	args := []string{"agy", "--dangerously-skip-permissions"}
	if a.Session != "" {
		args = append(args, "--conversation", a.Session)
	}
	// --add-dir, because cwd is ignored. bwrap is what actually confines it.
	args = append(args, "--add-dir", a.Workdir, "--model", a.Model)
	// --print LAST, because it consumes the next argument as the prompt.
	args = append(args, "--print", prompt)

	out, err := runWrapped(ctx, a, g.StateDirs(), args)
	if err != nil {
		return "", a.Session, err
	}

	session := a.Session
	if session == "" {
		if id := newConversation(before); id != "" {
			session = id
		}
		// Still empty means agy left us no id to resume: the driver falls back
		// to a full payload each turn, which works and merely costs a context
		// rebuild.
	}
	return out, session, nil
}

func conversationIDs() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(conversationsDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".db") {
			out[strings.TrimSuffix(filepath.Base(n), ".db")] = true
		}
	}
	return out
}

// newConversation returns the id that appeared since the snapshot. If more than
// one did — a human running agy at the same moment — we cannot tell which is
// ours, so we take none and go sessionless rather than resume a stranger's
// conversation.
func newConversation(before map[string]bool) string {
	var fresh []string
	for id := range conversationIDs() {
		if !before[id] {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) != 1 {
		return ""
	}
	sort.Strings(fresh)
	return fresh[0]
}
