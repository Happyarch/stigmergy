package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/deliberate"
	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
)

func newDeliberateCmd() *cobra.Command {
	var (
		seed      string
		out       string
		agentSpec []string
		maxRounds int
		only      []string
		yes       bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "deliberate",
		Short: "Put a specification through three agents that try to break it",
		Long: "Draft, interrogate, revise, tear down, judge — then rotate the agents left and do it\n" +
			"again until the judge upholds nothing or the rounds run out.\n\n" +
			"Each agent works at the real repository path, confined by bwrap to an overlay whose\n" +
			"writes land in RAM and die with the process. Your working tree is never touched and\n" +
			"does not need to be clean.\n\n" +
			"See docs/deliberation.md.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if seed == "" {
				return errors.New("--seed is required: the idea to specify, or @file to read one")
			}
			if out == "" {
				return errors.New("--out is required: where the final specification lands")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := gitx.ResolveWithFallback(cwd)
			if err != nil {
				if errors.Is(err, gitx.ErrNotARepo) {
					return fmt.Errorf("%s is not inside a git repository", cwd)
				}
				return err
			}
			// Every repository of the project by default. A spec that spans a
			// client and its service is the ordinary case for a multi-repo
			// project, and having to name the repositories every time would make
			// the common case the one that needs a flag.
			repos, err := deliberationRepos(cwd, repo.WorktreeRoot, only)
			if err != nil {
				return err
			}

			intent, err := readSeed(seed)
			if err != nil {
				return err
			}
			agents, err := parseAgents(agentSpec)
			if err != nil {
				return err
			}
			outPath := out
			if !filepath.IsAbs(outPath) {
				outPath = filepath.Join(repo.WorktreeRoot, outPath)
			}
			// --out is the one path this command writes in the real tree, and it
			// had no containment check at all: an absolute path, or enough "../",
			// silently wrote the spec anywhere the user could write. The flag is
			// documented as relative to the repository root, so hold it to that.
			if err := insideRepos(repos, outPath); err != nil {
				return err
			}

			// Checked BEFORE the preview, and long before an agent runs: the
			// alternative is discovering at minute forty, having spent twelve
			// turns, that the thing you were writing landed on something you
			// wanted. --out is the one path this command touches in the real
			// tree, so it is the one that has to be right.
			if err := checkOut(outPath, force); err != nil {
				return err
			}

			runID := newRunID()
			if err := preview(cmd.OutOrStdout(), agents, maxRounds, outPath, runID, yes); err != nil {
				return err
			}

			res, err := deliberate.Run(cmd.Context(), deliberate.Config{
				Repos:     repos,
				Primary:   repo.WorktreeRoot,
				Intent:    intent,
				Out:       outPath,
				Agents:    agents,
				MaxRounds: maxRounds,
				RunID:     runID,
			}, cmd.ErrOrStderr())
			if err != nil {
				// A turn stalled or crashed. Do not let one dead agent wipe the run:
				// if any spec was completed at a handoff before the failure, write it
				// to --out under an INTERRUPTED header so the round's work survives
				// and can be picked up by hand. res.Spec is empty only when the very
				// first draft never produced one — nothing to save, so nothing is.
				if res.Spec != "" {
					header := fmt.Sprintf(
						"<!-- deliberated: INTERRUPTED — a turn failed in round %d.\n"+
							"     error: %v\n"+
							"     This is the last spec completed at an agent handoff; it was NOT validated. -->\n\n",
						res.Rounds, err)
					if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkErr == nil {
						if wErr := os.WriteFile(outPath, []byte(header+res.Spec), 0o644); wErr == nil {
							fmt.Fprintf(cmd.OutOrStdout(),
								"\n%s\nINTERRUPTED in round %d — the last completed spec was preserved above.\n"+
									"per-turn transcripts: %s\n",
								outPath, res.Rounds, runDirDisplay(runID))
						}
					}
				}
				return err
			}

			// The label is not decoration. A spec that ran out of budget with
			// known flaws in it is a different artifact from one that survived an
			// attack, and prose alone will not tell them apart in a month.
			header := "<!-- deliberated: VALIDATED — the adversary found no flaws -->\n\n"
			if !res.Validated {
				header = fmt.Sprintf(
					"<!-- deliberated: UNVALIDATED — max rounds (%d) reached with %d finding(s) outstanding.\n"+
						"     This specification was NOT passed by the adversary. -->\n\n",
					res.Rounds, len(res.Critique.Findings))
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(outPath, []byte(header+res.Spec), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n%s after %d round(s)\nrun: %s\n",
				outPath, statusWord(res), res.Rounds, runDirDisplay(runID))
			if !res.Validated && len(res.Critique.Findings) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "\noutstanding:\n")
				for _, f := range res.Critique.Findings {
					fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s\n", f.Severity, f.Summary)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&seed, "seed", "", "the idea to specify, or @path to read it from a file")
	cmd.Flags().StringVar(&out, "out", "",
		"where the final specification is written, relative to the repo root (e.g. docs/specs/thing.md). "+
			"Written once at the end, labelled validated or unvalidated; the per-round artifacts stay in "+
			"the run directory under XDG state. Refuses to clobber an existing file unless --force")
	cmd.Flags().StringSliceVar(&agentSpec, "agents", nil,
		"rotation order: host[:model[:effort]], e.g. codex:gpt-5.4-mini:low,claude-code:sonnet:low")
	cmd.Flags().IntVar(&maxRounds, "max-rounds", 3, "give up after this many rounds")
	cmd.Flags().StringSliceVar(&only, "repo", nil,
		"limit the run to these repositories of the project (repeatable; default: all of them)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the cost confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite --out if it already exists")
	return cmd
}

// checkOut refuses to plan on top of something that already exists.
//
// The run ends by writing --out, so without this a mistyped path silently
// destroys a file forty minutes and twelve agent turns later — at the moment the
// user is least prepared to notice, reading a success message. Overwriting is
// still allowed; it just has to be asked for.
//
// A previous deliberation's output is the exception worth naming: re-running to
// improve a spec is the normal thing to want, and the header this command writes
// is proof the file is deliberate's own to replace.
func checkOut(path string, force bool) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("--out %s is a directory", path)
	}
	if force {
		return nil
	}
	if b, err := os.ReadFile(path); err == nil && strings.HasPrefix(string(b), "<!-- deliberated:") {
		return nil // written by deliberate, in a previous run
	}
	return fmt.Errorf("--out %s already exists and was not written by deliberate.\n"+
		"The run would overwrite it at the end, after every agent turn has been spent.\n"+
		"Pick another path, or pass --force if replacing it is what you want", path)
}

func statusWord(r deliberate.Result) string {
	if r.Validated {
		return "VALIDATED"
	}
	return "UNVALIDATED — max rounds reached"
}

func readSeed(seed string) (string, error) {
	if !strings.HasPrefix(seed, "@") {
		return seed, nil
	}
	b, err := os.ReadFile(strings.TrimPrefix(seed, "@"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseAgents reads host[:model[:effort]] triples.
func parseAgents(specs []string) ([]deliberate.Agent, error) {
	if len(specs) == 0 {
		return nil, errors.New("--agents is required, e.g. --agents codex:gpt-5.4-mini:low,claude-code:sonnet:low")
	}
	var out []deliberate.Agent
	for _, s := range specs {
		parts := strings.Split(s, ":")
		a := deliberate.Agent{Kind: parts[0]}
		if len(parts) > 1 {
			a.Model = parts[1]
		}
		if len(parts) > 2 {
			a.Effort = parts[2]
		}
		if a.Model == "" {
			return nil, fmt.Errorf("agent %q needs a model: host:model[:effort]", s)
		}
		if _, err := deliberate.NewAdapter(a.Kind); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// preview says what this is about to cost before a single token is spent.
//
// A round is five agent turns (the Guide's agent runs twice — once to interrogate,
// once to judge) and the default ceiling is three rounds, so the worst case is
// fifteen turns across three harnesses on a repository. That is not a thing to
// discover afterwards.
func preview(w interface{ Write([]byte) (int, error) }, agents []deliberate.Agent, maxRounds int, out, runID string, yes bool) error {
	fmt.Fprintf(w, "stigmergy deliberate — %d agent(s), up to %d round(s)\n\n", len(agents), maxRounds)
	fmt.Fprintf(w, "  planner/guide/adversary rotate over (the guide's agent also judges):\n")
	for i, a := range agents {
		effort := a.Effort
		if effort == "" {
			effort = "default"
		}
		fmt.Fprintf(w, "    %d. %-13s %-24s effort=%s\n", i+1, a.Kind, a.Model, effort)
	}
	fmt.Fprintf(w, "\n  each agent works at the real repository path, in a bwrap overlay whose\n")
	fmt.Fprintf(w, "  writes go nowhere. your working tree is not touched and need not be clean.\n\n")
	// Six, not five. A round is DRAFT, INTERROGATE, REVISE, TEARDOWN, ADJUDICATE
	// — and the Judge gets exactly one re-prompt when its verdict will not parse,
	// which is a sixth turn and is not rare. Under-quoting the ceiling is the
	// wrong direction to be wrong in when each turn is a frontier-model call.
	fmt.Fprintf(w, "  up to %d agent turns. output -> %s\n  run: %s\n", maxRounds*6, out, runDirDisplay(runID))

	if len(agents) < 3 {
		fmt.Fprintf(w, "\n  WARNING: fewer than 3 agents. Rotation still separates the roles, but with\n"+
			"  one mind in every slot its blind spots are perfectly correlated and the\n"+
			"  central defence of this design is gone.\n")
	}
	if dup := sameFamily(agents); dup != "" {
		fmt.Fprintf(w, "\n  WARNING: %s — two harnesses, one model family. Correlated blind spots\n"+
			"  rebuild the echo chamber through the back door.\n", dup)
	}

	if yes {
		return nil
	}
	fmt.Fprintf(w, "\n  press enter to start, or ctrl-c: ")
	var line string
	_, _ = fmt.Scanln(&line)
	return nil
}

// sameFamily catches the quiet one: agy serves Claude models, so a rotation of
// claude-code + antigravity:"Claude Sonnet 4.6" is one mind wearing two hats.
func sameFamily(agents []deliberate.Agent) string {
	seen := map[string]string{}
	for _, a := range agents {
		fam := family(a.Model)
		if fam == "" {
			continue
		}
		if prev, ok := seen[fam]; ok {
			return fmt.Sprintf("%s and %s are both %s", prev, a.Kind, fam)
		}
		seen[fam] = a.Kind
	}
	return ""
}

func family(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"), strings.Contains(m, "sonnet"),
		strings.Contains(m, "opus"), strings.Contains(m, "haiku"):
		return "claude"
	case strings.Contains(m, "gemini"):
		return "gemini"
	case strings.Contains(m, "gpt"):
		return "gpt"
	}
	return ""
}

// deliberationRepos decides which repositories a run may write in.
//
// Default: every repository of the project, because a spec that spans a client
// and its service is what a multi-repo project is FOR, and a worker that can
// read the second repository but not write it fails with EROFS partway through
// a plan. --repo narrows it when a run really is about one of them.
//
// A single-repository project resolves to exactly the repository it always did,
// so nothing here changes for the projects that have one.
func deliberationRepos(cwd, worktree string, only []string) ([]string, error) {
	proj, err := project.ResolveWithFallback(cwd)
	if err != nil || !proj.Adopted() {
		// Not adopted: deliberation does not require stigmergy, so fall back to
		// the repository the command was run in rather than refusing to run.
		if len(only) > 0 {
			return nil, fmt.Errorf("--repo needs a stigmergy project; run `stigmergy init` first")
		}
		return []string{worktree}, nil
	}

	db, err := store.OpenProjectAt(proj.DBPath, store.ReadOnly())
	if err != nil {
		return []string{worktree}, nil
	}
	defer db.Close()
	if err := proj.Load(db); err != nil || len(proj.Members) == 0 {
		return []string{worktree}, nil
	}

	if len(only) == 0 {
		out := make([]string, 0, len(proj.Members))
		for _, m := range proj.Members {
			out = append(out, m.WorktreeRoot)
		}
		return out, nil
	}

	var out []string
	for _, name := range only {
		m := proj.Member(name)
		if m == nil {
			return nil, fmt.Errorf("--repo %q is not a repository in this project (have: %s)",
				name, strings.Join(proj.MemberIDs(), ", "))
		}
		out = append(out, m.WorktreeRoot)
	}
	return out, nil
}

// runDirDisplay names this run's directory for a human.
//
// The run directory moved out of .git/deliberate when a deliberation stopped
// belonging to exactly one repository. Every message that pointed at the old
// place is now wrong, and a wrong path in a success message is how someone
// concludes the transcripts were never written.
func runDirDisplay(runID string) string {
	root, err := deliberate.DefaultRunRoot()
	if err != nil {
		return runID
	}
	return filepath.Join(root, runID)
}

// insideRepos refuses an --out that escapes every repository in the run.
//
// ANY member, not just the one you invoked from. A deliberation spanning a
// client and its service may perfectly well belong in the service's docs, and
// requiring the spec to land in whichever repository you happened to be standing
// in would be an arbitrary restriction on the exact case multi-repo exists for.
//
// Component-wise, and on the cleaned path, so "../" sequences are resolved before
// the comparison rather than pattern-matched in the string.
func insideRepos(repos []string, out string) error {
	for _, w := range repos {
		rel, err := filepath.Rel(filepath.Clean(w), filepath.Clean(out))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return nil
	}
	return fmt.Errorf("--out must stay inside one of this run's repositories (%s), got %s",
		strings.Join(repos, ", "), out)
}

func newRunID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("deliberate: system CSPRNG unavailable: " + err.Error())
	}
	return "d-" + hex.EncodeToString(b[:])
}
