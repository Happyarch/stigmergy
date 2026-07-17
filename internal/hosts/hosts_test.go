package hosts

import (
	"strings"
	"testing"
)

// The registry is the only declaration of a host, so the things every other
// package assumes about it are asserted here rather than discovered in
// production.

func TestEveryHostIsFullyDeclared(t *testing.T) {
	for _, h := range All() {
		if h.Kind == "" || h.Name == "" || h.Flag == "" {
			t.Errorf("host %+v is missing an identifier", h)
		}
		// An agent that cannot work out its own session_label registers under the
		// wrong one and is then blocked by its own claims. There is no sensible
		// default for this, so it must be said for every host.
		if h.SessionLabel == "" {
			t.Errorf("%s does not say where its session_label comes from", h.Name)
		}
	}
}

func TestKindsAndFlagsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, h := range All() {
		if prev, dup := seen["kind:"+h.Kind]; dup {
			t.Errorf("agent_kind %q is claimed by both %s and %s", h.Kind, prev, h.Name)
		}
		seen["kind:"+h.Kind] = h.Name
		if prev, dup := seen["flag:"+h.Flag]; dup {
			t.Errorf("--host %q is claimed by both %s and %s", h.Flag, prev, h.Name)
		}
		seen["flag:"+h.Flag] = h.Name
	}
}

// "all" is how init says every host. A host that took it as its own flag would
// be unreachable, and `--host all` would quietly mean something else.
func TestNoHostClaimsTheAllFlag(t *testing.T) {
	for _, h := range All() {
		if h.Flag == "all" {
			t.Errorf("%s uses the reserved flag \"all\"", h.Name)
		}
	}
}

func TestGetFindsEveryDeclaredKind(t *testing.T) {
	for _, kind := range Kinds() {
		if _, ok := Get(kind); !ok {
			t.Errorf("Kinds() offers %q but Get does not find it", kind)
		}
	}
	if _, ok := Get("cursor"); ok {
		t.Error("Get invented a host that was never declared")
	}
}

// The rules are what an agent acts on. A blank one is worse than a wrong one:
// it reads as "there is nothing to know here".
func TestEveryHostRendersEveryRule(t *testing.T) {
	for _, h := range All() {
		for name, rule := range map[string]string{
			"ClaimRule":    h.ClaimRule(),
			"MailRule":     h.MailRule(),
			"SubagentRule": h.SubagentRule(),
		} {
			if strings.TrimSpace(rule) == "" {
				t.Errorf("%s.%s is empty", h.Name, name)
			}
		}
	}
}

// The whole reason the rules are generated: a host that cannot block an edit
// must never be told that its edits are blocked. This is the sentence that,
// hand-copied between two files, said the opposite of the truth on one of them.
func TestAHostIsNeverPromisedEnforcementItDoesNotHave(t *testing.T) {
	for _, h := range All() {
		claim := h.ClaimRule()
		blocked := strings.Contains(claim, "blocked outright")
		if h.Claims == ClaimsBlocked && !blocked {
			t.Errorf("%s blocks edits but its rule does not say so: %q", h.Name, claim)
		}
		if h.Claims == ClaimsWarned && blocked {
			t.Errorf("%s cannot block an edit, but its rule claims edits are blocked outright: %q", h.Name, claim)
		}

		mail := h.MailRule()
		guaranteed := strings.Contains(mail, "cannot finish")
		if h.Mail == MailEnforced && !guaranteed {
			t.Errorf("%s enforces mail but its rule does not say so: %q", h.Name, mail)
		}
		if h.Mail == MailAdvisory && guaranteed {
			t.Errorf("%s cannot hold a turn open, but its rule promises mail is enforced: %q", h.Name, mail)
		}
	}
}

// Where the gate cannot fire, the text has to say so. An agent told the gate
// works will trust it, and on these hosts nothing is there to catch it.
func TestUnguardedHostsAreToldTheRuleIsOnlyHonoured(t *testing.T) {
	for _, h := range All() {
		rule := h.SubagentRule()
		if h.Subagents == SubagentsUnseen {
			if !strings.Contains(rule, "Nothing enforces this") {
				t.Errorf("%s cannot see subagents but its rule does not admit it: %q", h.Name, rule)
			}
			if !strings.Contains(rule, "stigmergy explore") {
				t.Errorf("%s should be pointed at `stigmergy explore`: %q", h.Name, rule)
			}
		}
		if h.Subagents == SubagentsGated && strings.Contains(rule, "Nothing enforces this") {
			t.Errorf("%s does gate subagents; its rule should not disclaim enforcement: %q", h.Name, rule)
		}
	}
}

func TestListReadsAsProse(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "no host"},
		{[]string{"Codex"}, "Codex"},
		{[]string{"Claude Code", "Antigravity"}, "Claude Code and Antigravity"},
		{[]string{"Claude Code", "Antigravity", "opencode"}, "Claude Code, Antigravity and opencode"},
	} {
		if got := List(tc.in); got != tc.want {
			t.Errorf("List(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNamesFiltersInRegistryOrder(t *testing.T) {
	got := Names(func(h Host) bool { return h.Claims == ClaimsBlocked })
	if len(got) == 0 {
		t.Fatal("no host blocks edits; the registry has lost Claude Code")
	}
	if got[0] != "Claude Code" {
		t.Errorf("Names should preserve registry order, got %v", got)
	}
}
