package mcpserver

import (
	"reflect"
	"strings"
	"testing"

	"github.com/happyarch/stigmergy/internal/hosts"
)

// The agent_kind description is the last place that repeats the host list.
//
// Everything else — the server instructions, the session-start text, the --host
// flag, store.AgentKinds — is rendered from the registry. This one cannot be: a
// jsonschema tag is a struct tag, and a struct tag is a compile-time constant.
// So it is pinned instead. It is exactly the kind of string that stayed at
// "claude-code, codex" through the whole of Antigravity's life.
func TestTheAgentKindSchemaNamesEveryHost(t *testing.T) {
	field, ok := reflect.TypeOf(RootRegisterInput{}).FieldByName("AgentKind")
	if !ok {
		t.Fatal("RootRegisterInput has no AgentKind field")
	}
	tag := field.Tag.Get("jsonschema")
	if tag == "" {
		t.Fatal("agent_kind has no jsonschema description; agents would be asked for a value with no hint what it may be")
	}

	for _, kind := range hosts.Kinds() {
		if !strings.Contains(tag, kind) {
			t.Errorf("host %q is missing from the agent_kind description; agents will not know they may pass it.\n"+
				"  tag: %s\n"+
				"  add it, or delete the list from the tag — but do not leave it half true.", kind, tag)
		}
	}
}
