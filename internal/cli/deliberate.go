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
)

func newDeliberateCmd() *cobra.Command {
	var (
		seed      string
		out       string
		agentSpec []string
		maxRounds int
		yes       bool
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "deliberate",
		Short: "Put a specification through three agents that try to break it",
		Long: "Draft, interrogate, revise, tear down — then rotate the agents left and do it again\n" +
			"until the adversary passes or the rounds run out.\n\n" +
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
				Repo:      repo.WorktreeRoot,
				Intent:    intent,
				Out:       outPath,
				Agents:    agents,
				MaxRounds: maxRounds,
				RunID:     runID,
			}, cmd.ErrOrStderr())
			if err != nil {
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

			fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n%s after %d round(s)\nrun: .git/deliberate/%s\n",
				outPath, statusWord(res), res.Rounds, runID)
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
			".git/deliberate/<run-id>/. Refuses to clobber an existing file unless --force")
	cmd.Flags().StringSliceVar(&agentSpec, "agents", nil,
		"rotation order: host[:model[:effort]], e.g. codex:gpt-5.4-mini:low,claude-code:sonnet:low")
	cmd.Flags().IntVar(&maxRounds, "max-rounds", 3, "give up after this many rounds")
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
// is proof the file is ours to replace.
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
		return nil // ours, from a previous run
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
// A round is four agent turns and the default ceiling is three rounds, so the
// worst case is twelve turns across three harnesses on a repository. That is not
// a thing to discover afterwards.
func preview(w interface{ Write([]byte) (int, error) }, agents []deliberate.Agent, maxRounds int, out, runID string, yes bool) error {
	fmt.Fprintf(w, "stigmergy deliberate — %d agent(s), up to %d round(s)\n\n", len(agents), maxRounds)
	fmt.Fprintf(w, "  planner/guide/adversary rotate over:\n")
	for i, a := range agents {
		effort := a.Effort
		if effort == "" {
			effort = "default"
		}
		fmt.Fprintf(w, "    %d. %-13s %-24s effort=%s\n", i+1, a.Kind, a.Model, effort)
	}
	fmt.Fprintf(w, "\n  each agent works at the real repository path, in a bwrap overlay whose\n")
	fmt.Fprintf(w, "  writes go nowhere. your working tree is not touched and need not be clean.\n\n")
	fmt.Fprintf(w, "  up to %d agent turns. output -> %s\n  run: .git/deliberate/%s\n", maxRounds*4, out, runID)

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

func newRunID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("deliberate: system CSPRNG unavailable: " + err.Error())
	}
	return "d-" + hex.EncodeToString(b[:])
}
