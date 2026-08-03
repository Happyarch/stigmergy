package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/store"
)

// callAs makes a tool call the way a host does when an agent inside the session
// is the one calling: the request carries the host's tool-use id in _meta, and
// the hook has already left a ticket under that id saying who it belongs to.
//
// The two halves are deliberately kept separate here, as they are in production:
// nothing in the request itself claims an identity, and nothing the model
// controls decides one.
func (h *harness) callAs(label, toolUseID, name string, args map[string]any) (map[string]any, map[string]any) {
	h.t.Helper()
	if label != "" {
		if err := h.session.project.PutCallerTicket(store.CallerTicket{
			ToolUseID: toolUseID, SessionLabel: label,
			AgentKind: "claude-code", Worktree: h.worktree,
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	res, err := h.client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: args,
		Meta: mcp.Meta{callerToolUseID: toolUseID},
	})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	text := textOf(h.t, res)
	body := map[string]any{}
	if text != "" {
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			h.t.Fatalf("%s: result is not JSON: %q", name, text)
		}
	}
	if res.IsError {
		errObj, _ := body["error"].(map[string]any)
		return nil, errObj
	}
	return body, nil
}

func claimOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	c, ok := out["claim"].(map[string]any)
	if !ok {
		t.Fatalf("no claim in %v", out)
	}
	return c
}

// The bug, end to end: two agents sharing one Claude Code session, one MCP
// connection and one session id, and a claim taken by one of them that does NOT
// belong to the other.
//
// Before caller tickets the server had no way to tell these two calls apart —
// they arrive on the same connection, with identical parameters but for the
// tool-use id — so the second agent was handed the first one's identity and its
// claims with it.
func TestPeersInOneSessionGetSeparateRootsAndSeparateClaims(t *testing.T) {
	h := newHarness(t)
	h.call("context_open", map[string]any{"project_root": h.worktree})
	reg := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "S",
	})
	sessionRoot := reg["root"].(map[string]any)["root_id"].(string)

	peerA := store.AgentLabel("S", "explorer", "a1")
	peerB := store.AgentLabel("S", "explorer", "b2")

	out, errObj := h.callAs(peerA, "toolu_1", "claim_acquire", map[string]any{
		"scope_path": "src/api.go", "reason": "peer A is editing it",
	})
	if errObj != nil {
		t.Fatalf("a peer was refused a claim — the whole bug: %v", errObj)
	}
	claimA := claimOf(t, out)
	rootA, _ := claimA["root_id"].(string)
	if rootA == sessionRoot {
		t.Fatal("the peer's claim was recorded under the session's root; the two are not distinguishable")
	}
	if own, _ := claimA["own"].(bool); !own {
		t.Error("a claim must come back marked as its taker's own")
	}

	// The neighbour: same session, same connection, different agent. It must see
	// the path as spoken for by someone else.
	check, errObj := h.callAs(peerB, "toolu_2", "claim_check", map[string]any{"path": "src/api.go"})
	if errObj != nil {
		t.Fatalf("claim_check: %v", errObj)
	}
	if claimed, _ := check["claimed"].(bool); !claimed {
		t.Fatal("peer B was told an unclaimed path")
	}
	found := check["claims"].([]any)[0].(map[string]any)
	if own, _ := found["own"].(bool); own {
		t.Fatal("peer B was told its neighbour's claim was its own — the failure this exists to prevent")
	}

	// And it cannot take the path either.
	if _, errObj := h.callAs(peerB, "toolu_3", "claim_acquire", map[string]any{
		"scope_path": "src/api.go", "reason": "peer B wants it too",
	}); errObj == nil {
		t.Fatal("two agents in one session were both granted the same path")
	}
}

// A call with no ticket is the main thread, and must behave exactly as it did
// before any of this existed. This is the compatibility guarantee: hosts with no
// hooks installed, other harnesses, and every call made before this shipped.
func TestAnUnstampedCallIsTheSessionItself(t *testing.T) {
	h := newHarness(t)
	h.call("context_open", map[string]any{"project_root": h.worktree})
	reg := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "S",
	})
	sessionRoot := reg["root"].(map[string]any)["root_id"].(string)

	out, errObj := h.callAs("", "toolu_unstamped", "claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "the main thread works",
	})
	if errObj != nil {
		t.Fatalf("claim_acquire: %v", errObj)
	}
	if got := claimOf(t, out)["root_id"].(string); got != sessionRoot {
		t.Fatalf("an unstamped call acted as %s, want the session root %s", got, sessionRoot)
	}
}

// A ticket answers one call. If it survived, the next unstamped call in the
// session — the main thread's — would silently inherit the peer's identity, and
// the main thread would find its own claims foreign to it.
func TestATicketDoesNotOutliveItsCall(t *testing.T) {
	h := newHarness(t)
	h.call("context_open", map[string]any{"project_root": h.worktree})
	reg := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "S",
	})
	sessionRoot := reg["root"].(map[string]any)["root_id"].(string)

	peer := store.AgentLabel("S", "explorer", "a1")
	if _, errObj := h.callAs(peer, "toolu_1", "claim_acquire", map[string]any{
		"scope_path": "src/api.go", "reason": "peer",
	}); errObj != nil {
		t.Fatalf("claim_acquire: %v", errObj)
	}

	// Same tool-use id, no fresh ticket: the host would never do this, which is
	// exactly why the server must not honour it.
	out, errObj := h.callAs("", "toolu_1", "claim_acquire", map[string]any{
		"scope_path": "src/main.go", "reason": "the main thread works",
	})
	if errObj != nil {
		t.Fatalf("claim_acquire: %v", errObj)
	}
	if got := claimOf(t, out)["root_id"].(string); got != sessionRoot {
		t.Fatalf("a consumed ticket was honoured again: acted as %s, want %s", got, sessionRoot)
	}
}

// The roster is what an agent reads to decide who to negotiate with, so "which
// of these is me" has to be answered for the agent that asked.
func TestTheRosterNamesThePeerAsItself(t *testing.T) {
	h := newHarness(t)
	h.call("context_open", map[string]any{"project_root": h.worktree})
	h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "S",
	})
	peer := store.AgentLabel("S", "explorer", "a1")
	out, _ := h.callAs(peer, "toolu_1", "claim_acquire", map[string]any{
		"scope_path": "src/api.go", "reason": "peer",
	})
	peerRoot := claimOf(t, out)["root_id"].(string)

	roster, errObj := h.callAs(peer, "toolu_2", "root_list_active", map[string]any{})
	if errObj != nil {
		t.Fatalf("root_list_active: %v", errObj)
	}
	var sawSelf bool
	for _, r := range roster["roots"].([]any) {
		row := r.(map[string]any)
		isYou, _ := row["is_you"].(bool)
		if row["root_id"].(string) == peerRoot {
			sawSelf = isYou
		} else if isYou {
			t.Errorf("the roster told the peer it was %s", row["root_id"])
		}
	}
	if !sawSelf {
		t.Error("the peer could not find itself in the roster")
	}
}
