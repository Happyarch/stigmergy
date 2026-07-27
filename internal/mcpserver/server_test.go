package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/store"
)

// harness wires a real client to a real server over in-memory transports, in a
// real git repo, so the tests exercise the same paths a host would.
type harness struct {
	t        *testing.T
	client   *mcp.ClientSession
	session  *Session
	worktree string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	worktree := t.TempDir()
	run(t, worktree, "init", "-b", "main")
	return newHarnessIn(t, worktree, filepath.Join(t.TempDir(), "global.sqlite3"))
}

// newHarnessIn attaches another independent session to an existing repository
// and global DB — two agents, one project, exactly as in production.
func newHarnessIn(t *testing.T, worktree, globalPath string) *harness {
	t.Helper()
	srv, session := New("test", globalPath)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close(); session.Close() })

	return &harness{t: t, client: cs, session: session, worktree: worktree}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// call invokes a tool and returns its structured output, failing if the tool
// reported an error.
func (h *harness) call(name string, args map[string]any) map[string]any {
	h.t.Helper()
	res, out, errBody := h.tryCall(name, args)
	if errBody != nil {
		h.t.Fatalf("%s failed: %v", name, errBody)
	}
	if res.IsError {
		h.t.Fatalf("%s reported an error", name)
	}
	return out
}

// tryCall invokes a tool and, if it failed, decodes the JSON error body the
// protocol promises. Returning the parsed error is the point: agents branch on
// the code, so the tests assert on the code too.
func (h *harness) tryCall(name string, args map[string]any) (*mcp.CallToolResult, map[string]any, map[string]any) {
	h.t.Helper()
	res, err := h.client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	text := textOf(h.t, res)
	if res.IsError {
		var body struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			h.t.Fatalf("%s: error content is not the promised JSON body: %q", name, text)
		}
		if body.Error == nil {
			h.t.Fatalf("%s: error body has no \"error\" object: %q", name, text)
		}
		return res, nil, body.Error
	}
	out := map[string]any{}
	if text != "" {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			h.t.Fatalf("%s: result content is not JSON: %q", name, text)
		}
	}
	return res, out, nil
}

func (h *harness) errCode(name string, args map[string]any) string {
	h.t.Helper()
	_, _, e := h.tryCall(name, args)
	if e == nil {
		h.t.Fatalf("%s: expected an error, got success", name)
	}
	code, _ := e["code"].(string)
	return code
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func (h *harness) open() { h.call("context_open", map[string]any{"project_root": h.worktree}) }

func (h *harness) register() string {
	h.t.Helper()
	out := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "sess-1",
	})
	root := out["root"].(map[string]any)
	return root["root_id"].(string)
}

// The state machine is what stops an unidentified agent from mutating shared
// state, so every gate gets asserted, not just the happy path.
func TestStateMachineGating(t *testing.T) {
	h := newHarness(t)

	// UNOPENED: reads and writes alike must refuse, and say what to call.
	unopened := map[string]map[string]any{
		"memory_list":   {"scope": "project"},
		"memory_search": {"query": "x"},
		"memory_write": {
			"scope": "project", "key": "k", "type": "project", "description": "d", "body": "b",
		},
		"root_register": {"agent_kind": "claude-code", "worktree": h.worktree},
	}
	for tool, args := range unopened {
		if code := h.errCode(tool, args); code != "wrong_state" {
			t.Fatalf("%s before context_open: code=%q, want wrong_state", tool, code)
		}
	}

	h.open()

	// OPENED: reads work, mutations still refuse.
	h.call("memory_list", map[string]any{"scope": "project"})
	if code := h.errCode("memory_write", map[string]any{
		"scope": "project", "key": "k", "type": "project", "description": "d", "body": "b",
	}); code != "wrong_state" {
		t.Fatalf("memory_write before root_register: code=%q, want wrong_state", code)
	}
	if code := h.errCode("root_heartbeat", map[string]any{}); code != "wrong_state" {
		t.Fatalf("root_heartbeat before root_register: code=%q, want wrong_state", code)
	}

	// REGISTERED: everything is available.
	h.register()
	h.call("memory_write", map[string]any{
		"scope": "project", "key": "k", "type": "project", "description": "d", "body": "b",
	})
	h.call("root_heartbeat", map[string]any{})
}

func TestContextOpenRejectsBadRoots(t *testing.T) {
	h := newHarness(t)

	if code := h.errCode("context_open", map[string]any{"project_root": "relative/path"}); code != "invalid_input" {
		t.Fatalf("relative project_root: code=%q, want invalid_input", code)
	}
	if code := h.errCode("context_open", map[string]any{"project_root": t.TempDir()}); code != "not_a_repo" {
		t.Fatalf("non-repo project_root: code=%q, want not_a_repo", code)
	}

	out := h.call("context_open", map[string]any{"project_root": h.worktree})
	if out["project_common_dir"] == "" || out["schema_version"].(float64) < 1 {
		t.Fatalf("context_open returned %#v", out)
	}
	// Re-opening the same repo must not discard the session's root.
	id := h.register()
	again := h.call("context_open", map[string]any{"project_root": h.worktree})
	if again["reopened"] != true {
		t.Fatalf("re-opening the same repo should report reopened: %#v", again)
	}
	h.call("memory_write", map[string]any{
		"scope": "project", "key": "still-registered", "type": "project", "description": "d", "body": "b",
	})
	if h.session.root.RootID != id {
		t.Fatal("re-opening the same repo stranded the registered root")
	}
}

// Resume is what stops a host restart from stranding a session's claims.
func TestRootRegisterResumesSameSession(t *testing.T) {
	h := newHarness(t)
	h.open()

	first := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "sess-A",
	})
	firstID := first["root"].(map[string]any)["root_id"].(string)
	if first["resumed"] != false {
		t.Fatal("the first registration is not a resume")
	}

	second := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "sess-A",
	})
	if second["resumed"] != true {
		t.Fatalf("re-registering the same session must resume: %#v", second)
	}
	if got := second["root"].(map[string]any)["root_id"].(string); got != firstID {
		t.Fatalf("resume minted a new root %q, want %q — the old root's claims would be stranded", got, firstID)
	}

	// A different session label is a different agent, and must not resume.
	other := h.call("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": h.worktree, "session_label": "sess-B",
	})
	if other["resumed"] != false || other["root"].(map[string]any)["root_id"] == firstID {
		t.Fatalf("a different session_label must mint a new root: %#v", other)
	}

	// Codex in the same worktree with the same label is still a different agent.
	codex := h.call("root_register", map[string]any{
		"agent_kind": "codex", "worktree": h.worktree, "session_label": "sess-A",
	})
	if codex["resumed"] != false {
		t.Fatal("a different agent_kind must not resume another host's root")
	}
}

func TestRootRegisterValidatesAgentKind(t *testing.T) {
	h := newHarness(t)
	h.open()
	if code := h.errCode("root_register", map[string]any{
		"agent_kind": "cursor", "worktree": h.worktree,
	}); code != "invalid_input" {
		t.Fatalf("unknown agent_kind: code=%q, want invalid_input", code)
	}
}

// worktree is a required field, and it must not be quietly defaulted. A root
// resumes on (agent_kind, worktree, session_label), so registering under a
// worktree the caller never named would make the next session's resume miss and
// strand this root's claims until they timed out.
func TestRootRegisterRejectsEmptyWorktree(t *testing.T) {
	h := newHarness(t)
	h.open()
	if code := h.errCode("root_register", map[string]any{
		"agent_kind": "claude-code", "worktree": "", "session_label": "sess-a",
	}); code != "invalid_input" {
		t.Fatalf("empty worktree: code=%q, want invalid_input", code)
	}
}

func TestMemoryRoundTripThroughProtocol(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	created := h.call("memory_write", map[string]any{
		"scope": "project", "key": "build-system", "type": "project",
		"description": "How the build works", "body": "Uses go build with CGO disabled.",
	})
	if created["created"] != true {
		t.Fatalf("write = %#v, want created", created)
	}

	// A create over an existing key must conflict and hand back the current
	// entry, so the agent can merge instead of guessing.
	_, _, e := h.tryCall("memory_write", map[string]any{
		"scope": "project", "key": "build-system", "type": "project",
		"description": "d", "body": "b",
	})
	if e == nil || e["code"] != "cas_conflict" {
		t.Fatalf("blind re-create: %#v, want cas_conflict", e)
	}
	cur, ok := e["current"].(map[string]any)
	if !ok || cur["version"].(float64) != 1 {
		t.Fatalf("cas_conflict did not carry the current entry: %#v", e)
	}

	// Updating at the version just returned must succeed.
	updated := h.call("memory_write", map[string]any{
		"scope": "project", "key": "build-system", "type": "project",
		"description": "How the build works", "body": "Uses go build, static.",
		"expected_version": 1,
	})
	if updated["created"] != false || updated["memory"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("update = %#v", updated)
	}

	read := h.call("memory_read", map[string]any{"scope": "project", "key": "build-system"})
	if read["found"] != true || read["memory"].(map[string]any)["body"] != "Uses go build, static." {
		t.Fatalf("read = %#v", read)
	}
	missing := h.call("memory_read", map[string]any{"scope": "project", "key": "nope"})
	if missing["found"] != false {
		t.Fatalf("reading a missing key must report found:false, got %#v", missing)
	}

	hits := h.call("memory_search", map[string]any{"query": "static build"})["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["scope"] != "project" {
		t.Fatalf("search = %#v", hits)
	}

	// memory_list must stay bodyless: the index is what keeps context cheap.
	entries := h.call("memory_list", map[string]any{"scope": "project"})["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("list = %#v", entries)
	}
	if _, leaked := entries[0].(map[string]any)["body"]; leaked {
		t.Fatal("memory_list leaked bodies")
	}

	// Delete is CAS-guarded.
	if code := h.errCode("memory_delete", map[string]any{
		"scope": "project", "key": "build-system", "expected_version": 1,
	}); code != "cas_conflict" {
		t.Fatalf("stale delete: code=%q, want cas_conflict", code)
	}
	del := h.call("memory_delete", map[string]any{
		"scope": "project", "key": "build-system", "expected_version": 2,
	})
	if del["deleted"] != true {
		t.Fatalf("delete = %#v", del)
	}
	if h.call("memory_read", map[string]any{"scope": "project", "key": "build-system"})["found"] != false {
		t.Fatal("memory survived delete")
	}
}

func TestMemoryWriteSuggestsNearDuplicatesOnCreate(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	h.call("memory_write", map[string]any{
		"scope": "project", "key": "build-system", "type": "project",
		"description": "How the build works", "body": "Uses go build.",
	})
	out := h.call("memory_write", map[string]any{
		"scope": "project", "key": "build-config", "type": "project",
		"description": "How the build is configured", "body": "Flags live in the Makefile.",
	})
	similar, ok := out["similar"].([]any)
	if !ok || len(similar) == 0 {
		t.Fatalf("creating a near-duplicate must surface the existing entry: %#v", out)
	}
	if similar[0].(map[string]any)["key"] != "build-system" {
		t.Fatalf("similar = %#v, want build-system", similar)
	}
	// A memory must never suggest itself.
	for _, s := range similar {
		if s.(map[string]any)["key"] == "build-config" {
			t.Fatal("write suggested the entry it just created")
		}
	}
}

func TestMemoryPromoteThroughProtocol(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	h.call("memory_write", map[string]any{
		"scope": "project", "key": "review-style", "type": "user",
		"description": "Review preference", "body": "Prefers small diffs.",
	})
	out := h.call("memory_promote", map[string]any{"key": "review-style", "expected_version": 1})
	if out["created"] != true {
		t.Fatalf("promote = %#v", out)
	}
	if h.call("memory_read", map[string]any{"scope": "global", "key": "review-style"})["found"] != true {
		t.Fatal("promoted memory is not readable in the global scope")
	}
	// Promotion is a copy: the project keeps its entry.
	if h.call("memory_read", map[string]any{"scope": "project", "key": "review-style"})["found"] != true {
		t.Fatal("promote removed the project entry")
	}

	// The global scope really is a separate database.
	if hits := h.call("memory_search", map[string]any{
		"query": "small diffs", "scopes": []string{"global"},
	})["hits"].([]any); len(hits) != 1 || hits[0].(map[string]any)["scope"] != "global" {
		t.Fatalf("global-scoped search = %#v", hits)
	}
}

// The time surface has to survive the protocol, not just the store: the fields
// only exist for agents if the JSON schema carries them.
func TestMemoryListTimeSurfaceThroughProtocol(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()

	for _, m := range []struct{ key, at string }{
		{"first-note", "2026-01-01T00:00:00Z"},
		{"second-note", "2026-03-01T00:00:00Z"},
		{"third-note", "2026-05-01T00:00:00Z"},
	} {
		when, err := time.Parse(time.RFC3339, m.at)
		if err != nil {
			t.Fatalf("bad fixture time: %v", err)
		}
		restore := store.SetClock(func() time.Time { return when })
		h.call("memory_write", map[string]any{
			"scope": "project", "key": m.key, "type": "project",
			"description": "note " + m.key, "body": "body of " + m.key,
		})
		restore()
	}

	keys := func(args map[string]any) []string {
		h.t.Helper()
		var out []string
		for _, e := range h.call("memory_list", args)["entries"].([]any) {
			out = append(out, e.(map[string]any)["key"].(string))
		}
		return out
	}

	if got := keys(map[string]any{"scope": "project", "order_by": "recent"}); strings.Join(got, ",") !=
		"third-note,second-note,first-note" {
		t.Errorf("order_by recent = %v", got)
	}
	if got := keys(map[string]any{
		"scope": "project", "updated_since": "2026-02-01T00:00:00Z",
	}); strings.Join(got, ",") != "second-note,third-note" {
		t.Errorf("updated_since = %v", got)
	}
	// Omitting the fields entirely must keep the old behaviour exactly.
	if got := keys(map[string]any{"scope": "project"}); strings.Join(got, ",") !=
		"first-note,second-note,third-note" {
		t.Errorf("default order = %v, want key order", got)
	}

	for name, args := range map[string]map[string]any{
		"unparseable bound": {"scope": "project", "updated_since": "last tuesday"},
		"unknown order_by":  {"scope": "project", "order_by": "freshness"},
		"inverted range": {"scope": "project",
			"updated_since": "2026-05-01T00:00:00Z", "updated_before": "2026-01-01T00:00:00Z"},
	} {
		if code := h.errCode("memory_list", args); code != "invalid_input" {
			t.Errorf("%s: code=%q, want invalid_input", name, code)
		}
	}
}

// The timestamp rides along with a search hit, so an agent can see how old a
// result is without a second call.
func TestMemorySearchHitCarriesUpdatedAt(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	h.call("memory_write", map[string]any{
		"scope": "project", "key": "mounting-order", "type": "reference",
		"description": "the overlay comes first", "body": "Apply the tmpfs after the overlay.",
	})
	hits := h.call("memory_search", map[string]any{"query": "tmpfs"})["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if at, _ := hits[0].(map[string]any)["updated_at"].(string); at == "" {
		t.Fatalf("search hit carries no updated_at: %#v", hits[0])
	}
}

func TestScopeValidation(t *testing.T) {
	h := newHarness(t)
	h.open()
	if code := h.errCode("memory_list", map[string]any{"scope": "local"}); code != "invalid_input" {
		t.Fatalf("bad scope: code=%q, want invalid_input", code)
	}
}

func TestDeregisterEndsRoot(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	if out := h.call("root_deregister", map[string]any{}); out["ended"] != true {
		t.Fatalf("deregister = %#v", out)
	}
	// After deregistering, the session is merely OPENED again: reads work,
	// mutations do not.
	h.call("memory_list", map[string]any{"scope": "project"})
	if code := h.errCode("memory_write", map[string]any{
		"scope": "project", "key": "k", "type": "project", "description": "d", "body": "b",
	}); code != "wrong_state" {
		t.Fatalf("write after deregister: code=%q, want wrong_state", code)
	}
}

// Codex only prioritizes the first 512 characters of a server's instructions,
// so the mandatory workflow has to fit inside them. This is a budget, and it is
// easy to blow by accident.
func TestPriorityInstructionsFitBudget(t *testing.T) {
	if n := len(priority); n > PriorityBudget {
		t.Fatalf("priority instructions are %d bytes, over the %d-byte budget", n, PriorityBudget)
	}
	head := Instructions()[:PriorityBudget]
	for _, must := range []string{"context_open", "root_register", "memory_search", "claim_", "expected_version"} {
		if !strings.Contains(head, must) {
			t.Errorf("the prioritized window does not mention %q — agents will miss it", must)
		}
	}
}

func TestToolsAreAdvertisedWithSchemas(t *testing.T) {
	h := newHarness(t)
	tools, err := h.client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"context_open": false, "root_register": false, "root_heartbeat": false,
		"root_list_active": false,
		"root_deregister":  false, "memory_search": false, "memory_read": false,
		"memory_list": false, "memory_write": false, "memory_promote": false,
		"memory_delete": false, "memory_evidence_set": false, "memory_evidence_clear": false,
		"memory_verify": false, "memory_history": false,
		"claim_acquire": false, "claim_check": false,
		"claim_list_active": false, "claim_renew": false, "claim_release": false,
		"mailbox_send": false, "mailbox_inbox": false, "mailbox_thread": false, "mailbox_threads": false,
		"mailbox_mark_read": false, "mailbox_resolve": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		want[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q was not advertised", name)
		}
	}
}

// The server must never write to stdout: stdout is the protocol channel.
func TestGlobalDBLivesUnderTheGivenPath(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.register()
	h.call("memory_write", map[string]any{
		"scope": "global", "key": "machine-fact", "type": "user",
		"description": "d", "body": "b",
	})
	if _, err := os.Stat(h.session.globalPath); err != nil {
		t.Fatalf("global DB was not created at the configured path: %v", err)
	}
}
