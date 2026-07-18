package hooks

import (
	"strings"
	"testing"
)

// TestSessionStartStatesFactsWhenHostSelfRegisters: when the host exports its
// session (Claude Code sets CLAUDE_CODE_SESSION_ID), the MCP server has already
// registered the root, so the text must state that as a fact and not send the
// agent chasing a context_open/root_register handshake that already happened.
//
// But stating the fact is not enough, and getting only that far is the bug this
// guards: a message that says "you are registered, nothing to do" reads as ambient
// FYI and the agent never searches memory or claims a file. So the message must
// still lead with the concrete first actions AND must not undercut them as
// optional.
func TestSessionStartStatesFactsWhenHostSelfRegisters(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-abc")
	f := newFixture(t)

	text := SessionStartText("claude-code", "sess-abc", f.worktree)
	if text == "" {
		t.Fatal("session-start went silent for an adopted project")
	}
	if !strings.Contains(text, "already registered") {
		t.Errorf("self-registered session was not told so:\n%s", text)
	}
	if strings.Contains(text, "Register now") {
		t.Errorf("a self-registered agent was told to register anyway:\n%s", text)
	}
	// The two calls that make a self-registered agent an actual participant must
	// be spelled out — stating registration is done is not a substitute for them.
	for _, want := range []string{"memory_search", "claim_acquire"} {
		if !strings.Contains(text, want) {
			t.Errorf("self-registered agent was not given its first action %q:\n%s", want, text)
		}
	}
	// And they must not be framed away as optional. This phrase is the tell of the
	// old message, which is why the agent ignored it.
	if strings.Contains(text, "nothing depends on it") {
		t.Errorf("the self-register message still undercuts its own instructions:\n%s", text)
	}
	// The host-specific claim rule must still be there — self-registration does
	// not change what the guard can do to an edit.
	if !strings.Contains(text, "blocked outright") {
		t.Errorf("Claude Code's claim rule went missing:\n%s", text)
	}
}

// TestSessionStartAsksToRegisterWithoutHostSession: absent a host session in the
// environment — a non-Claude host, or a Claude that has stopped exporting it —
// the MCP server cannot self-register, so the agent must still be told to.
func TestSessionStartAsksToRegisterWithoutHostSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	f := newFixture(t)

	text := SessionStartText("claude-code", "sess-abc", f.worktree)
	if !strings.Contains(text, "Register now") {
		t.Errorf("without a host session, the agent was not told to register:\n%s", text)
	}
	if !strings.Contains(text, "root_register(") {
		t.Errorf("the registration call was not spelled out:\n%s", text)
	}
}
