// Package deliberate runs a specification through three agents that never talk
// to each other.
//
// One drafts, one interrogates, one attacks, and on every failed round they
// shift left so the mind resolving a teardown is never the mind that wrote it.
// They coordinate entirely through traces left in a shared environment — a spec,
// a question list, a critique — which is what the word stigmergy means and why
// this lives here.
//
// See docs/deliberation.md. This package is the walking skeleton of that spec:
// the round loop, the rotation, the sandbox and the verdict. Intake, resume,
// PAUSED and the mailbox are not built yet.
package deliberate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Config is one run.
type Config struct {
	Repo      string  // worktree root of the repository being planned about
	Intent    string  // the seed / brief. the user's words.
	Out       string  // absolute path for the final spec
	Agents    []Agent // rotation order. len >= 1.
	MaxRounds int
	RunID     string
}

// Result is what a run produced.
type Result struct {
	Spec      string
	Validated bool // true only if the Adversary passed
	Rounds    int
	Critique  Verdict // the surviving critique when unvalidated
}

// Run drives the pipeline to a verdict or to the round ceiling.
func Run(ctx context.Context, cfg Config, log io.Writer) (Result, error) {
	if err := RequireBwrap(); err != nil {
		return Result{}, err
	}
	if len(cfg.Agents) == 0 {
		return Result{}, errors.New("deliberation needs at least one agent")
	}
	if cfg.MaxRounds < 1 {
		cfg.MaxRounds = 3
	}

	adapters := make([]Adapter, len(cfg.Agents))
	for i := range cfg.Agents {
		a, err := NewAdapter(cfg.Agents[i].Kind)
		if err != nil {
			return Result{}, err
		}
		adapters[i] = a
	}

	// Every agent works at the real repository path. There is nothing to set up
	// and nothing to tear down: isolation is the per-process overlay
	// ([BwrapArgs]), which means each turn gets its own invisible copy-on-write
	// layer and the layer dies with the process.
	//
	// This replaced a worktree per slot. Worktrees cost a checkout, a sweep, a
	// prune and an orphan bug when a run was killed — and they showed the worker
	// HEAD rather than the working tree, hiding uncommitted work and any project
	// skill that had not been committed. A killed run now leaves nothing behind
	// because there was never anything on disk to leave.
	for i := range cfg.Agents {
		cfg.Agents[i].Slot = i
		cfg.Agents[i].Workdir = cfg.Repo
	}

	// Create the run directory before the first turn, not lazily on the first
	// artifact: the sandbox mounts a tmpfs over it to keep one agent's turns out
	// of another's reach ([BwrapArgs]), and bwrap needs the mountpoint to exist.
	if err := os.MkdirAll(runDir(cfg), 0o700); err != nil {
		return Result{}, err
	}

	p := Payload{Intent: cfg.Intent}
	var last Verdict
	rotation := 0

	for round := 1; round <= cfg.MaxRounds; round++ {
		fmt.Fprintf(log, "\n── round %d/%d ──\n", round, cfg.MaxRounds)

		// role(i) = agents[(rotation+i) % n]. The array is never reordered:
		// shift-left is an offset. Reordering it would mean moving each agent's
		// session in lockstep, and the first off-by-one would hand one agent's
		// transcript to another. Nothing would crash; the run would just quietly
		// stop being three minds.
		planner := pick(cfg.Agents, rotation, 0)
		guide := pick(cfg.Agents, rotation, 1)
		adversary := pick(cfg.Agents, rotation, 2)

		spec, err := turn(ctx, cfg, adapters, planner, Planner, p, log)
		if err != nil {
			return Result{}, err
		}
		p.Spec = spec
		p.Critique = "" // consumed by the draft; the spec now contains its resolution

		questions, err := turn(ctx, cfg, adapters, guide, Guide, p, log)
		if err != nil {
			return Result{}, err
		}
		p.Questions = questions

		revised, err := turn(ctx, cfg, adapters, planner, Planner, p, log)
		if err != nil {
			return Result{}, err
		}
		p.Spec = revised
		p.Questions = ""

		v, err := teardown(ctx, cfg, adapters, adversary, p, log)
		if err != nil {
			return Result{}, err
		}
		last = v
		p.Critique = v.Raw

		if v.Pass {
			fmt.Fprintf(log, "\nPASS — the adversary found nothing.\n")
			return Result{Spec: p.Spec, Validated: true, Rounds: round}, nil
		}
		fmt.Fprintf(log, "FAIL — %d finding(s)\n", len(v.Findings))

		if round < cfg.MaxRounds {
			rotation++
			fmt.Fprintf(log, "shift left: planner is now slot %d\n", pick(cfg.Agents, rotation, 0).Slot)
		}
	}

	return Result{Spec: p.Spec, Validated: false, Rounds: cfg.MaxRounds, Critique: last}, nil
}

func pick(agents []Agent, rotation, role int) *Agent {
	return &agents[(rotation+role)%len(agents)]
}

func turn(ctx context.Context, cfg Config, adapters []Adapter, a *Agent, r Role, p Payload, log io.Writer) (string, error) {
	fmt.Fprintf(log, "  %-9s %s … ", r, a)
	start := time.Now()

	out, session, err := adapters[a.Slot].Turn(ctx, a, Prompt(r, p))
	if err != nil {
		fmt.Fprintln(log, "failed")
		return "", err
	}
	a.Session = session

	fmt.Fprintf(log, "%d chars in %s\n", len(out), time.Since(start).Round(time.Second))
	writeArtifact(cfg, fmt.Sprintf("slot%d-%s", a.Slot, r), out)
	return out, nil
}

// teardown is the Adversary's turn plus the only parse that can end a run.
//
// A malformed verdict gets exactly one re-prompt, on the same session so the
// agent can see what it wrote, and then the round counts as FAIL. Every
// ambiguity resolves toward more scrutiny: a false FAIL costs one round, a false
// PASS ships a spec that nothing attacked.
func teardown(ctx context.Context, cfg Config, adapters []Adapter, a *Agent, p Payload, log io.Writer) (Verdict, error) {
	out, err := turn(ctx, cfg, adapters, a, Adversary, p, log)
	if err != nil {
		return Verdict{}, err
	}
	v, err := ParseVerdict(out)
	if err == nil {
		return v, nil
	}

	fmt.Fprintf(log, "  (no verdict line — re-prompting once)\n")
	retry, err := turn(ctx, cfg, adapters, a, Adversary, Payload{
		Intent: p.Intent,
		Spec:   p.Spec,
		Critique: "Your last output had no verdict. Reply with ONLY the fenced json block " +
			"and a final line reading exactly `VERDICT: PASS` or `VERDICT: FAIL`.",
	}, log)
	if err != nil {
		return Verdict{}, err
	}
	v, err = ParseVerdict(retry)
	if err != nil {
		fmt.Fprintf(log, "  (still no verdict — counting the round as FAIL)\n")
		return Verdict{Pass: false, Raw: out}, nil
	}
	return v, nil
}

// runDir is where this run's turns are kept. Under .git/ so it is never
// committed, and hidden from the workers by the sandbox.
func runDir(cfg Config) string {
	return filepath.Join(cfg.Repo, ".git", "deliberate", cfg.RunID)
}

func writeArtifact(cfg Config, name, body string) {
	if cfg.RunID == "" {
		return
	}
	if os.MkdirAll(runDir(cfg), 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(runDir(cfg), name+".md"), []byte(body), 0o600)
}
