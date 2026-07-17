package hooks

import (
	"strings"
	"testing"
)

// TestSessionStartStatesFactsWhenHostSelfRegisters: when the host exports its
// session (Claude Code sets CLAUDE_CODE_SESSION_ID), the MCP server has already
// registered the root, so the text must state that as a fact and not send the
// agent chasing a context_open/root_register handshake that already happened.
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
