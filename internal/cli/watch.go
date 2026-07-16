package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
)

func newWatchCmd() *cobra.Command {
	var (
		follow   bool
		asJSON   bool
		interval time.Duration
		threads  int
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Show who is working here, what they have claimed, and what they are saying",
		Long: "Show the live state of this project: the agents currently working in it, the claims\n" +
			"they hold, and the conversations between them.\n\n" +
			"This is the human's window onto a mesh that otherwise only agents can see. It reads;\n" +
			"it never writes, and it never marks anything as read.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := gitx.ResolveWithFallback(cwd)
			if err != nil {
				return fmt.Errorf("%s is not inside a git repository", cwd)
			}
			if _, err := os.Stat(store.ProjectDBPath(repo.CommonDir)); err != nil {
				return fmt.Errorf("stigmergy is not enabled here — run `stigmergy init`")
			}
			// Read-only: watching must never create, migrate, or lock anything.
			// A viewer that can break the thing it is watching is worse than no
			// viewer.
			db, err := store.OpenProject(repo.CommonDir, store.ReadOnly())
			if err != nil {
				return fmt.Errorf("could not open the project database: %w", err)
			}
			defer db.Close()

			out := cmd.OutOrStdout()
			for {
				snap, err := snapshot(db, threads)
				if err != nil {
					return err
				}
				if asJSON {
					enc := json.NewEncoder(out)
					enc.SetIndent("", "  ")
					if err := enc.Encode(snap); err != nil {
						return err
					}
				} else {
					if follow {
						fmt.Fprint(out, "\033[H\033[2J") // home, clear
					}
					renderSnapshot(out, snap)
				}
				if !follow {
					return nil
				}
				time.Sleep(interval)
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching, redrawing as things change")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the snapshot as JSON")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "how often to refresh with --follow")
	cmd.Flags().IntVar(&threads, "threads", 10, "how many recent conversations to show")
	return cmd
}

// Snapshot is everything happening in a project at one moment.
type Snapshot struct {
	At      string         `json:"at"`
	Roots   []store.Root   `json:"roots"`
	Claims  []store.Claim  `json:"claims"`
	Threads []store.Thread `json:"threads"`
}

func snapshot(db *store.DB, threadLimit int) (*Snapshot, error) {
	roots, err := db.ActiveRoots()
	if err != nil {
		return nil, err
	}
	// selfRoot is empty: the watcher is not an agent, so no claim is "its own".
	claims, err := db.ActiveClaims("")
	if err != nil {
		return nil, err
	}
	threads, err := db.AllThreads(threadLimit)
	if err != nil {
		return nil, err
	}
	return &Snapshot{At: store.Now(), Roots: roots, Claims: claims, Threads: threads}, nil
}

func renderSnapshot(out io.Writer, s *Snapshot) {
	now, _ := store.ParseStamp(s.At)

	fmt.Fprintf(out, "ROOTS (%d active)\n", len(s.Roots))
	if len(s.Roots) == 0 {
		fmt.Fprintln(out, "  nobody is working here right now")
	}
	for _, r := range s.Roots {
		fmt.Fprintf(out, "  %-14s %-12s %-18s %-14s %-12s seen %s\n",
			r.RootID, r.AgentKind, dash(r.Model), dash(r.Branch), dash(r.SessionLabel),
			since(now, r.LastSeenAt))
	}

	fmt.Fprintf(out, "\nCLAIMS (%d)\n", len(s.Claims))
	if len(s.Claims) == 0 {
		fmt.Fprintln(out, "  nothing is claimed")
	}
	for _, c := range s.Claims {
		scope := c.ScopePath
		if c.Recursive {
			scope += "/**"
		}
		fmt.Fprintf(out, "  %-24s %-14s %q  expires %s\n",
			scope, c.RootID, c.Reason, until(now, c.ExpiresAt))
	}

	fmt.Fprintf(out, "\nMAILBOX (%d threads)\n", len(s.Threads))
	if len(s.Threads) == 0 {
		fmt.Fprintln(out, "  no one has needed to negotiate")
	}
	for _, t := range s.Threads {
		state := strings.ToUpper(t.State)
		if t.ClaimID != nil {
			fmt.Fprintf(out, "\n  #%d %s  (claim %d)\n", t.ID, state, *t.ClaimID)
		} else {
			fmt.Fprintf(out, "\n  #%d %s\n", t.ID, state)
		}
		if t.Resolution != "" {
			fmt.Fprintf(out, "     resolved: %s\n", t.Resolution)
		}
		for _, m := range t.Messages {
			unread := ""
			if m.ReadAt == "" {
				unread = "  UNREAD"
			}
			fmt.Fprintf(out, "     %s -> %s  %s%s\n", m.FromRoot, m.ToRoot, since(now, m.SentAt), unread)
			fmt.Fprintf(out, "       %s\n", m.Subject)
			for _, line := range wrap(m.Body, 68) {
				fmt.Fprintf(out, "         %s\n", line)
			}
		}
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// since renders how long ago something happened. A watcher wants "4s ago", not a
// timestamp it has to subtract in its head.
func since(now time.Time, stamp string) string {
	t, err := store.ParseStamp(stamp)
	if err != nil {
		return stamp
	}
	return short(now.Sub(t)) + " ago"
}

func until(now time.Time, stamp string) string {
	t, err := store.ParseStamp(stamp)
	if err != nil {
		return stamp
	}
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return "in " + short(d)
}

func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// wrap breaks a message body onto readable lines. Agents write paragraphs, and a
// 400-character line makes the whole view unreadable.
func wrap(body string, width int) []string {
	var lines []string
	for _, para := range strings.Split(strings.TrimSpace(body), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len(line)+1+len(w) > width {
				lines = append(lines, line)
				line = w
				continue
			}
			line += " " + w
		}
		lines = append(lines, line)
	}
	return lines
}
