package deliberate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds one turn. A worker that never returns must not hold up
// the run forever.
//
// This is a blunt wall-clock ceiling, not an idle timeout: output is buffered and
// only read when the process exits, so a turn that is productively churning looks
// the same as one that is hung. 90 minutes is deliberately generous — a high-effort
// agentic DRAFT grounding itself in a large repo can run a long time, and killing
// live work costs a whole round. The real fix is an inactivity timeout on the
// host's event stream; until then, err toward letting a slow turn finish.
const DefaultTimeout = 90 * time.Minute

// runWrapped executes one host invocation inside bwrap and returns its stdout.
//
// Every host launch goes through here. There is no code path that starts a
// worker unwrapped, and that is deliberate: a fallback would be the one
// configuration nobody tested, taken automatically at the moment the boundary
// failed.
func runWrapped(ctx context.Context, a *Agent, stateDirs []string, cmd []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	sb := a.Confine
	sb.Chdir = a.Workdir
	sb.StateDirs = stateDirs
	sb.PayloadPath = a.PayloadPath
	if len(sb.Repos) == 0 {
		sb.Repos = []string{a.Workdir}
	}
	args := BwrapArgs(sb, cmd)
	c := exec.CommandContext(ctx, "bwrap", args...)
	c.Dir = a.Workdir

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()

	if ctx.Err() == context.DeadlineExceeded {
		// Almost always a permission prompt: it does not announce itself as one,
		// it announces itself as silence. agy's --print-timeout defaults to five
		// minutes, so an agent waiting on a dialog nobody can see just stops.
		return "", fmt.Errorf("%s exceeded its %s timeout with no output — if this repeats, suspect a "+
			"permission prompt: a blocked worker goes quiet rather than erroring", a, DefaultTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", a, err, tail(stderr.String(), 400))
	}
	return stdout.String(), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
