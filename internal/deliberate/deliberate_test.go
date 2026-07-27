package deliberate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeAdapter is a host that costs nothing.
//
// Adapter is three methods and the driver's entire job — the five-turn round,
// the rotation offset, what each role is and is not shown, the judge re-prompt —
// sits above it. Until Config.NewAdapter existed, reaching any of that meant
// spending real frontier-model turns, which is why none of it was tested and why
// the stale cost arithmetic survived so long.
type fakeAdapter struct {
	mu sync.Mutex
	// reply decides what this slot says, given the turn it is being asked for.
	reply func(t recordedTurn) string
	// seen is every turn this slot was asked to take, in order.
	seen *[]recordedTurn
}

type recordedTurn struct {
	Slot    int
	Kind    string
	Role    string
	Payload string // the brief as actually written for this turn
}

func (f *fakeAdapter) Kind() string        { return "fake" }
func (f *fakeAdapter) StateDirs() []string { return nil }

func (f *fakeAdapter) Turn(_ context.Context, a *Agent, prompt string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// The adapter is handed a short handoff and finds the real brief on disk.
	// Reading it here is what lets the tests below assert the payload contract.
	body, err := os.ReadFile(a.PayloadPath)
	if err != nil {
		return "", "", fmt.Errorf("fake adapter could not read its brief: %w", err)
	}
	rt := recordedTurn{Slot: a.Slot, Kind: a.Kind, Role: roleOf(string(body)), Payload: string(body)}
	*f.seen = append(*f.seen, rt)
	return f.reply(rt), fmt.Sprintf("session-%d", a.Slot), nil
}

// roleOf recovers which role a brief was written for, by its opening prompt.
func roleOf(body string) string {
	for _, r := range []Role{Planner, Guide, Adversary, Judge} {
		if strings.HasPrefix(body, roleHeader(r)) {
			return r.String()
		}
	}
	return "unknown"
}

func roleHeader(r Role) string {
	full := Prompt(r, Payload{})
	if i := strings.Index(full, "\n"); i > 0 {
		return full[:i]
	}
	return full
}

// harness builds a run whose agents are all fakes driven by one reply function.
func harness(t *testing.T, agents int, maxRounds int, reply func(recordedTurn) string) (Config, *[]recordedTurn) {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := &[]recordedTurn{}

	cfg := Config{
		Repos:     []string{repo},
		Intent:    "a tiny CLI that prints the current git branch",
		RunID:     "d-test",
		RunRoot:   filepath.Join(t.TempDir(), "runs"),
		MaxRounds: maxRounds,
		NewAdapter: func(string) (Adapter, error) {
			return &fakeAdapter{reply: reply, seen: seen}, nil
		},
	}
	for i := 0; i < agents; i++ {
		cfg.Agents = append(cfg.Agents, Agent{Kind: "fake", Model: fmt.Sprintf("m%d", i)})
	}
	return cfg, seen
}

func passOn(role string) func(recordedTurn) string {
	return func(rt recordedTurn) string {
		if rt.Role == role {
			return "Looks fine.\n\n```json\n{\"findings\":[]}\n```\n\nVERDICT: PASS"
		}
		return "# Spec\n\nSomething plausible for the " + rt.Role + " turn."
	}
}

// A round is five turns in a fixed order, and the Guide's agent takes two of
// them. The cost preview quotes a multiple of this, so it is worth pinning.
func TestARoundIsFiveTurnsInOrder(t *testing.T) {
	cfg, seen := harness(t, 3, 1, passOn("judge"))

	if _, err := Run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var got []string
	for _, rt := range *seen {
		got = append(got, rt.Role)
	}
	want := []string{"planner", "guide", "planner", "adversary", "judge"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("round order = %v, want %v", got, want)
	}
}

// Rotation is an OFFSET into a slice that is never reordered. Reordering would
// mean moving each agent's session in lockstep, and the first off-by-one hands
// one agent's transcript to another — silently, with nothing crashing.
func TestRotationShiftsRolesLeftWithoutReorderingAgents(t *testing.T) {
	cfg, seen := harness(t, 3, 2, func(rt recordedTurn) string {
		// Never pass, so the run uses both rounds.
		if rt.Role == "judge" {
			return "```json\n{\"findings\":[{\"title\":\"x\",\"detail\":\"y\"}]}\n```\n\nVERDICT: FAIL"
		}
		return "# Spec\n\nstill working"
	})

	if _, err := Run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Round 1: planner=slot0, guide=slot1, adversary=slot2 (judge is the guide's).
	// Round 2 shifts left by one: planner=slot1, guide=slot2, adversary=slot0.
	type rs struct {
		role string
		slot int
	}
	var got []rs
	for _, rt := range *seen {
		got = append(got, rs{rt.Role, rt.Slot})
	}
	want := []rs{
		{"planner", 0}, {"guide", 1}, {"planner", 0}, {"adversary", 2}, {"judge", 1},
		{"planner", 1}, {"guide", 2}, {"planner", 1}, {"adversary", 0}, {"judge", 2},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rotation:\n got %v\nwant %v", got, want)
	}
}

// THE payload contract (docs/deliberation.md §5). The whole premise — that
// rotation defeats the echo chamber — rests on each role seeing only what its
// job needs. An Adversary shown the Guide's questions is being handed the
// Planner's reasoning, and it stops being an independent attack.
func TestTheAdversaryNeverSeesTheGuidesQuestions(t *testing.T) {
	const secret = "GUIDE-QUESTION-CANARY"
	cfg, seen := harness(t, 3, 1, func(rt recordedTurn) string {
		switch rt.Role {
		case "guide":
			return secret
		case "judge":
			return "```json\n{\"findings\":[]}\n```\n\nVERDICT: PASS"
		default:
			return "# Spec\n\nplausible"
		}
	})

	if _, err := Run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, rt := range *seen {
		if rt.Role == "adversary" && strings.Contains(rt.Payload, secret) {
			t.Fatalf("the adversary's brief contained the guide's questions:\n%s", rt.Payload)
		}
	}
}

// The critique is consumed by the draft that resolves it. A Planner carrying
// every past teardown would drift toward writing for the Adversary rather than
// for the intent.
func TestOnlyTheMostRecentCritiqueReachesThePlanner(t *testing.T) {
	const old = "ROUND-ONE-CRITIQUE-CANARY"
	round := 0
	cfg, seen := harness(t, 3, 2, func(rt recordedTurn) string {
		switch rt.Role {
		case "adversary":
			round++
			if round == 1 {
				return old
			}
			return "a different complaint"
		case "judge":
			return "```json\n{\"findings\":[{\"title\":\"t\",\"detail\":\"d\"}]}\n```\n\nVERDICT: FAIL"
		default:
			return "# Spec\n\nplausible"
		}
	})

	if _, err := Run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The second round's planner must not still be carrying round one's teardown.
	var planners int
	for _, rt := range *seen {
		if rt.Role != "planner" {
			continue
		}
		planners++
		if planners > 2 && strings.Contains(rt.Payload, old) {
			t.Fatalf("a later planner still carried round one's critique:\n%s", rt.Payload)
		}
	}
}

// A malformed verdict costs exactly one re-prompt, then the round is FAIL. The
// parser must never guess PASS: a false FAIL costs one round, a false PASS ships
// a spec whose flaws nobody ruled on.
func TestAMalformedVerdictIsRepromptedOnceThenFails(t *testing.T) {
	judgeTurns := 0
	cfg, seen := harness(t, 3, 1, func(rt recordedTurn) string {
		if rt.Role == "judge" {
			judgeTurns++
			return "I think it's fine, honestly." // no verdict line, ever
		}
		return "# Spec\n\nplausible"
	})

	res, err := Run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Validated {
		t.Error("an unparseable verdict was treated as PASS")
	}
	if judgeTurns != 2 {
		t.Errorf("judge ran %d times, want 2 (one turn plus exactly one re-prompt)", judgeTurns)
	}
	// Six turns in a round that needed the re-prompt — which is why the cost
	// preview quotes maxRounds*6 rather than *5.
	if len(*seen) != 6 {
		t.Errorf("round took %d turns, want 6 with the re-prompt", len(*seen))
	}
}

// A PASS ends the run immediately; later rounds are not spent.
func TestAPassEndsTheRunAtThatRound(t *testing.T) {
	cfg, seen := harness(t, 3, 3, passOn("judge"))

	res, err := Run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Validated || res.Rounds != 1 {
		t.Errorf("Validated=%v Rounds=%d, want true/1", res.Validated, res.Rounds)
	}
	if len(*seen) != 5 {
		t.Errorf("%d turns spent, want 5 — a PASS must not run further rounds", len(*seen))
	}
}

// Every turn's brief is written where the driver can keep it, and a worker is
// handed a short pointer rather than the text. This is the argv ceiling fix:
// MAX_ARG_STRLEN caps ONE argument at 131072 bytes, and the payload crosses two
// execve boundaries on the way to the host.
func TestTheBriefGoesToAFileAndNotIntoArgv(t *testing.T) {
	huge := strings.Repeat("x", 200_000)
	cfg, seen := harness(t, 1, 1, func(rt recordedTurn) string {
		if rt.Role == "judge" {
			return "```json\n{\"findings\":[]}\n```\n\nVERDICT: PASS"
		}
		return "# Spec\n\n" + huge
	})

	if _, err := Run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("Run with a 200KB spec: %v", err)
	}

	// The brief really did get large...
	var biggest int
	for _, rt := range *seen {
		if len(rt.Payload) > biggest {
			biggest = len(rt.Payload)
		}
	}
	if biggest < 200_000 {
		t.Fatalf("largest brief was %d bytes; the test did not exercise the ceiling", biggest)
	}
	// ...while what a host receives on argv stayed a fixed sentence.
	if len(Handoff) > 4096 {
		t.Errorf("the argv handoff is %d bytes; it must stay far below MAX_ARG_STRLEN", len(Handoff))
	}
}

// The bind must survive the /tmp tmpfs, or the worker finds an empty file. This
// is the ordering rule the sandbox already lives by, applied to the payload.
func TestThePayloadBindComesAfterTheTmpfs(t *testing.T) {
	args := BwrapArgs(Sandbox{Repos: []string{"/repo"}, Chdir: "/repo", StateDirs: nil, PayloadPath: "/host/path/brief.md"}, []string{"true"})

	tmpfs := indexOfPair(args, "--tmpfs", "/tmp")
	bind := indexOfPair(args, "--ro-bind", "/host/path/brief.md")
	if tmpfs < 0 || bind < 0 {
		t.Fatalf("missing mounts: tmpfs=%d bind=%d in %v", tmpfs, bind, args)
	}
	if bind < tmpfs {
		t.Errorf("the payload bind (%d) precedes the /tmp tmpfs (%d); the tmpfs would mask it", bind, tmpfs)
	}
	if !contains(args, SandboxPayloadPath) {
		t.Errorf("the payload is not bound at %s: %v", SandboxPayloadPath, args)
	}
}

func TestNoPayloadMeansNoBind(t *testing.T) {
	args := BwrapArgs(Sandbox{Repos: []string{"/repo"}, Chdir: "/repo", StateDirs: nil, PayloadPath: ""}, []string{"true"})
	if contains(args, SandboxPayloadPath) {
		t.Errorf("a bind appeared with no payload: %v", args)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
