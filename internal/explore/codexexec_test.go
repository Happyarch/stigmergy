package explore

import (
	"slices"
	"strings"
	"testing"
)

// TestArgsNeverPassesAskForApproval is a regression test with a specific bug
// behind it, and the bug is worth stating because the shape of it will recur.
//
// Args passed `--ask-for-approval never` for months. It reads correctly, it
// matches every example in the wild, and it is exactly what you would write from
// memory. It is also a flag `codex exec` does not have: `--ask-for-approval` is
// top-level only — the interactive TUI, where there is a human to ask — and clap
// rejects an unknown flag outright:
//
//	error: unexpected argument '--ask-for-approval' found
//
// So `stigmergy explore` did not degrade. It died on argument parsing, before
// the model was ever reached, every single time. Nothing caught it because
// nothing tested Args and nothing runs codex in CI.
//
// The approval policy still has to be set — exec defaults to a read-only sandbox
// but the policy belongs in config — so the equivalent is `-c
// approval_policy="never"`, which is a real key whose value codex validates
// (untrusted|on-failure|on-request|granular|never).
func TestArgsNeverPassesAskForApproval(t *testing.T) {
	args := Args(Request{Prompt: "look around"})
	if slices.Contains(args, "--ask-for-approval") {
		t.Fatalf("Args passes --ask-for-approval, which `codex exec` rejects as an unknown "+
			"argument — explore dies before it starts. Set approval_policy via -c instead.\ngot: %v", args)
	}
}

// TestArgsConfinesTheExplorer pins every flag that makes an explorer an
// explorer. Each one is load-bearing (see Args' doc comment), and each is the
// kind of thing a well-meaning cleanup removes because it looks redundant.
func TestArgsConfinesTheExplorer(t *testing.T) {
	args := Args(Request{Prompt: "look around"})

	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("Args must invoke the exec subcommand; got %v", args)
	}

	pairs := []struct{ flag, want, why string }{
		{"--sandbox", "read-only", "an explorer that can write the tree can dodge the claim guard"},
		{"-c", `approval_policy="never"`, "escalation in a non-interactive run must fail closed, not prompt into a void"},
	}
	for _, p := range pairs {
		if !hasPair(args, p.flag, p.want) {
			t.Errorf("missing %s %s: %s\ngot: %v", p.flag, p.want, p.why, args)
		}
	}

	if !slices.Contains(args, "--ephemeral") {
		t.Errorf("missing --ephemeral: exploration must not carry session state between runs\ngot: %v", args)
	}

	// The explorer must not be able to see stigmergy's tools at all: no
	// registering, no claiming, no writing memory behind the root's back.
	if !hasPair(args, "-c", "mcp_servers.stigmergy.enabled=false") {
		t.Errorf("missing -c mcp_servers.stigmergy.enabled=false: the explorer could register a "+
			"root, take a claim, or write a memory\ngot: %v", args)
	}

	// The prompt goes after `--`, so a prompt beginning with a dash is a prompt
	// and not a flag.
	i := slices.Index(args, "--")
	if i == -1 {
		t.Fatalf("the prompt must be separated by --, or a prompt starting with a dash parses as a flag\ngot: %v", args)
	}
	if got := args[i+1:]; len(got) != 1 || got[0] != "look around" {
		t.Errorf("the prompt must be the only thing after --; got %v", got)
	}
}

// TestArgsJSONIsOptional pins --json to the request, since the caller decides
// whether it is parsing events or showing a human the output.
func TestArgsJSONIsOptional(t *testing.T) {
	if slices.Contains(Args(Request{Prompt: "p"}), "--json") {
		t.Error("--json must not be passed unless requested")
	}
	if !slices.Contains(Args(Request{Prompt: "p", JSON: true}), "--json") {
		t.Error("--json must be passed when requested")
	}
}

// hasPair reports whether flag is immediately followed by want.
//
// Membership alone is not enough: `-c` appears more than once with different
// values, so a Contains check would pass on args where the value belongs to a
// different flag entirely.
func hasPair(args []string, flag, want string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == want {
			return true
		}
	}
	return false
}

// TestArgsQuotesTheApprovalValue guards a trap in codex's -c parsing: values are
// parsed as TOML, so a bare `approval_policy=never` is not the string "never".
// Worse, an unknown key or an unparseable value is accepted *silently* unless
// --strict-config is passed — so getting this wrong does not fail, it just
// quietly leaves the policy at its default.
func TestArgsQuotesTheApprovalValue(t *testing.T) {
	for _, a := range Args(Request{Prompt: "p"}) {
		if strings.HasPrefix(a, "approval_policy=") && a != `approval_policy="never"` {
			t.Errorf(`approval_policy must be TOML-quoted as approval_policy="never"; got %s`, a)
		}
	}
}
