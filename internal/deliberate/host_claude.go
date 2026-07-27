package deliberate

import (
	"context"
	"crypto/rand"
	"fmt"
)

// claudeAdapter drives `claude -p`.
type claudeAdapter struct{}

func (claudeAdapter) Kind() string { return "claude-code" }

// StateDirs: the session store, plus the top-level config file claude keeps
// beside it. Both must be writable or --resume finds nothing.
func (claudeAdapter) StateDirs() []string {
	return []string{home(".claude"), home(".claude.json")}
}

func (c claudeAdapter) Turn(ctx context.Context, a *Agent, prompt string) (string, string, error) {
	session := a.Session
	args := []string{"claude", "-p"}
	if session == "" {
		// Claude is the only host that lets us pre-assign an id, which is the
		// cleanest of the four: there is no window where a turn has run but the
		// driver does not know how to get back to it, and nothing has to be
		// parsed out of the output.
		session = newUUID()
		args = append(args, "--session-id", session)
	} else {
		args = append(args, "--resume", session)
	}

	// acceptEdits, NOT bypassPermissions.
	//
	// bypassPermissions is the obvious read of "never prompt" — it says so in
	// the name — and it *escapes*: it wrote into the real repository from inside
	// the workspace it was given, because Claude Code has no sandbox and bypass means bypass,
	// the working directory included. acceptEdits auto-accepts edits inside the
	// workspace and returns "permission denied" for anything outside it, without
	// prompting and without hanging.
	//
	// bwrap makes this choice non-load-bearing (§6.4) — which is the point of
	// putting the boundary in the kernel — but there is no reason to hand a
	// worker a bigger hammer than the job needs.
	// Every member repository, not just the one we chdir into. bwrap already
	// overlays them all, but --add-dir is what decides which paths claude's own
	// tools will touch — without it the worker is blocked by its own host from
	// editing a sibling repository the sandbox is perfectly happy to let it write.
	for _, d := range a.WorkDirs() {
		args = append(args, "--add-dir", d)
	}
	args = append(args, "--permission-mode", "acceptEdits", "--model", a.Model)
	if a.Effort != "" {
		args = append(args, "--effort", a.Effort)
	}
	args = append(args, "--", prompt)

	out, err := runWrapped(ctx, a, c.StateDirs(), args)
	if err != nil {
		return "", a.Session, err
	}
	return out, session, nil
}

// newUUID is a v4 UUID. claude validates the shape, so this is not free-form.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failing is not a condition worth a code path
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
