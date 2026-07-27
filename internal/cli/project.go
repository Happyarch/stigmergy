package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/ids"
	"github.com/happyarch/stigmergy/internal/project"
	"github.com/happyarch/stigmergy/internal/store"
	"github.com/happyarch/stigmergy/internal/xdg"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Group several git repositories into one stigmergy project",
		Long: "A project is normally one repository, and nothing here is needed for that.\n\n" +
			"Use these when several repositories are one piece of work — a client and its\n" +
			"service, say — and changes cross between them. Members share one database, so\n" +
			"memories, the roster and the mailbox reach across all of them, and a claim in one\n" +
			"blocks an agent editing it from another.\n\n" +
			"The repositories need share no parent directory and may sit on different disks.",
		SilenceUsage: true,
	}
	cmd.AddCommand(newProjectCreateCmd(), newProjectAddCmd(),
		newProjectRemoveCmd(), newProjectListCmd())
	return cmd
}

// resolveMember turns a path into the repository at it, for membership.
func resolveMember(path string) (*gitx.Repo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	repo, err := gitx.ResolveWithFallback(abs)
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository", abs)
	}
	return repo, nil
}

// checkJoinable refuses a repository that already belongs somewhere.
//
// This is the "not silently captured" guarantee at the UX layer, and it is the
// mirror of the one in project.Resolve, which never walks up looking for a
// project. A repository joins because someone said so here, and it cannot be
// taken from a project it is already in by accident.
func checkJoinable(repo *gitx.Repo, absorb, fresh bool) error {
	if ptr, ok, err := project.ReadPointer(repo.CommonDir); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%s already belongs to project %s (as %q).\n"+
			"Remove it from that project first: `stigmergy project remove %s`",
			repo.WorktreeRoot, ptr.ProjectID, ptr.RepoID, ptr.RepoID)
	}

	dbPath := store.ProjectDBPath(repo.CommonDir)
	info, err := os.Stat(dbPath)
	if err != nil || info.Size() == 0 {
		return nil // nothing here to lose
	}
	if absorb || fresh {
		return nil
	}
	n := memoryCount(dbPath)
	return fmt.Errorf("%s already has its own stigmergy database (%s, %d memories).\n"+
		"Joining a project means its memories live in the project database instead. Choose:\n"+
		"  --absorb   import those memories into the project, then retire the old database\n"+
		"  --fresh    leave them behind (the old database is kept, unused)",
		repo.WorktreeRoot, dbPath, n)
}

func memoryCount(dbPath string) int {
	db, err := store.OpenProjectAt(dbPath, store.ReadOnly())
	if err != nil {
		return 0
	}
	defer db.Close()
	n, _ := countMemories(db)
	return n
}

// absorbMemories copies a repository's own memories into the project database.
//
// Best-effort per key and never destructive: a key that already exists in the
// project wins, because the project's copy is the one other members have been
// reading. The old database is left on disk rather than deleted — retiring a
// repository into a project should not be the thing that destroys its history.
func absorbMemories(out io.Writer, dbPath string, into *store.DB, actor string) {
	old, err := store.OpenProjectAt(dbPath, store.ReadOnly())
	if err != nil {
		return
	}
	defer old.Close()

	list, err := old.ListMemories()
	if err != nil {
		return
	}
	var moved, skipped int
	for _, m := range list {
		full, err := old.ReadMemory(m.Key)
		if err != nil {
			continue
		}
		if _, err := into.WriteMemory(store.MemoryWrite{
			Key: full.Key, Type: full.Type, Description: full.Description,
			Body: full.Body, UpdatedBy: actor,
		}, string(store.Project)); err != nil {
			// A key already in the project wins: the project's copy is the one
			// other members have been reading, and a repository joining late does
			// not get to overwrite it.
			skipped++
			continue
		}
		moved++
	}
	fmt.Fprintf(out, "  absorbed %d memories (%d already present in the project)\n", moved, skipped)
}

func newProjectCreateCmd() *cobra.Command {
	var (
		add           []string
		name          string
		host          string
		absorb, fresh bool
		noHosts       bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a project spanning several repositories",
		Long: "Mint a project, point each repository at it, and wire the agent hosts in each.\n\n" +
			"The repositories share one database, kept outside all of them so no repository is\n" +
			"the special one. They need share no parent directory.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validHost(host) {
				return fmt.Errorf("--host must be all or one of %s, got %q",
					strings.Join(hostFlagList(), ", "), host)
			}
			if len(add) < 1 {
				return fmt.Errorf("--add is required: name at least one repository (repeatable)")
			}
			if absorb && fresh {
				return fmt.Errorf("--absorb and --fresh are opposites; choose one")
			}
			out := cmd.OutOrStdout()

			// Resolve and vet everything BEFORE writing anything. Half a project
			// is worse than none: pointers in some repositories and not others is
			// a state nothing knows how to read.
			type member struct {
				repo *gitx.Repo
				id   string
			}
			var members []member
			seen := map[string]bool{}
			for _, path := range add {
				repo, err := resolveMember(path)
				if err != nil {
					return err
				}
				if seen[repo.CommonDir] {
					return fmt.Errorf("%s was named twice", repo.WorktreeRoot)
				}
				seen[repo.CommonDir] = true
				if err := checkJoinable(repo, absorb, fresh); err != nil {
					return err
				}
				id := project.SlugFor(repo.WorktreeRoot)
				if err := project.ValidateMemberID(id); err != nil {
					return err
				}
				members = append(members, member{repo, id})
			}
			for i := range members {
				for j := range members {
					if i != j && members[i].id == members[j].id {
						return fmt.Errorf("two repositories would both be called %q (%s and %s); "+
							"rename one directory, or add them one at a time with `project add --name`",
							members[i].id, members[i].repo.WorktreeRoot, members[j].repo.WorktreeRoot)
					}
				}
			}

			projectID := ids.NewProjectID()
			stateDir, err := project.StateDir(projectID)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				return err
			}
			dbPath, err := project.DBPathFor(projectID)
			if err != nil {
				return err
			}
			db, err := store.OpenProjectAt(dbPath)
			if err != nil {
				return fmt.Errorf("could not create the project database: %w", err)
			}
			defer db.Close()

			if name == "" {
				name = members[0].id
			}
			if err := db.SetMeta(store.MetaNameKey, name); err != nil {
				return err
			}
			fmt.Fprintf(out, "project %q (%s)\n  database %s\n\n", name, projectID, dbPath)

			for _, m := range members {
				if err := db.AddRepo(m.id, m.repo.CommonDir, m.repo.WorktreeRoot); err != nil {
					return err
				}
				if err := project.WritePointer(m.repo.CommonDir, project.Pointer{
					ProjectID: projectID, RepoID: m.id, CreatedAt: store.Now(),
				}); err != nil {
					return err
				}
				fmt.Fprintf(out, "  %-24s %s\n", m.id, m.repo.WorktreeRoot)

				if absorb {
					absorbMemories(out, store.ProjectDBPath(m.repo.CommonDir), db, "project-create")
				}
				if !noHosts {
					if err := installHosts(out, m.repo.WorktreeRoot, host); err != nil {
						return err
					}
				}
			}

			if globalPath, err := xdg.GlobalDBPath(); err == nil {
				if g, err := store.OpenGlobal(globalPath); err == nil {
					_ = g.RememberProject(dbPath, name)
					g.Close()
				}
			}

			fmt.Fprintln(out, "\nRestart any running agent sessions so they pick up the new configuration.")
			fmt.Fprintln(out, "Check it with `stigmergy doctor`.")
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&add, "add", nil, "a repository to include (repeatable)")
	cmd.Flags().StringVar(&name, "name", "", "what to call this project in listings (default: the first repository's name)")
	cmd.Flags().StringVar(&host, "host", "all", "which hosts to configure in each repository")
	cmd.Flags().BoolVar(&absorb, "absorb", false, "import each repository's existing memories into the project")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "leave existing per-repository memories behind")
	cmd.Flags().BoolVar(&noHosts, "no-hosts", false, "do not write host configuration")
	return cmd
}

func newProjectAddCmd() *cobra.Command {
	var (
		name          string
		host          string
		absorb, fresh bool
		noHosts       bool
	)
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Add a repository to the project you are in",
		Args:  cobra.ExactArgs(1),
		Long: "Run this from inside a member of the project, naming the repository to add.\n\n" +
			"It refuses a repository that already belongs to another project: membership is\n" +
			"always something someone stated, never something inferred.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Same gate as `project create` and `init`. Unvalidated, a typo matched
			// none of installHosts' branches, so the command reported a repository
			// added and wired nothing at all in it.
			if !validHost(host) {
				return fmt.Errorf("--host must be all or one of %s, got %q",
					strings.Join(hostFlagList(), ", "), host)
			}
			if absorb && fresh {
				return fmt.Errorf("--absorb and --fresh are opposites; choose one")
			}
			out := cmd.OutOrStdout()
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			here, err := project.ResolveWithFallback(cwd)
			if err != nil {
				return err
			}
			if !here.MultiRepo() {
				return fmt.Errorf("%s is a single-repository project.\n"+
					"Create a multi-repository project first:\n"+
					"  stigmergy project create --add . --add %s", here.Repo.WorktreeRoot, args[0])
			}

			repo, err := resolveMember(args[0])
			if err != nil {
				return err
			}
			if err := checkJoinable(repo, absorb, fresh); err != nil {
				return err
			}
			if name == "" {
				name = project.SlugFor(repo.WorktreeRoot)
			}
			if err := project.ValidateMemberID(name); err != nil {
				return err
			}

			db, err := store.OpenProjectAt(here.DBPath)
			if err != nil {
				return err
			}
			defer db.Close()

			if err := db.AddRepo(name, repo.CommonDir, repo.WorktreeRoot); err != nil {
				return err
			}
			if err := project.WritePointer(repo.CommonDir, project.Pointer{
				ProjectID: here.ID, RepoID: name, CreatedAt: store.Now(),
			}); err != nil {
				return err
			}
			fmt.Fprintf(out, "added %s as %q to project %s\n", repo.WorktreeRoot, name, here.ID)

			if absorb {
				absorbMemories(out, store.ProjectDBPath(repo.CommonDir), db, "project-add")
			}
			if !noHosts {
				if err := installHosts(out, repo.WorktreeRoot, host); err != nil {
					return err
				}
			}
			fmt.Fprintln(out, "\nRestart any running agent sessions so they pick up the new configuration.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "what to call this repository in scopes (default: its directory name)")
	cmd.Flags().StringVar(&host, "host", "all", "which hosts to configure")
	cmd.Flags().BoolVar(&absorb, "absorb", false, "import the repository's existing memories into the project")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "leave the repository's existing memories behind")
	cmd.Flags().BoolVar(&noHosts, "no-hosts", false, "do not write host configuration")
	return cmd
}

func newProjectRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <repo>",
		Short: "Remove a repository from the project",
		Args:  cobra.ExactArgs(1),
		Long: "Drops the repository's membership and releases every claim it held.\n\n" +
			"Releasing is not optional: a claim scoped to a repository nobody can resolve any\n" +
			"more would block edits with no way to negotiate it away, because its owner cannot\n" +
			"release what it can no longer name.\n\n" +
			"The project's memories stay with the project.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			here, err := project.ResolveWithFallback(cwd)
			if err != nil {
				return err
			}
			db, err := store.OpenProjectAt(here.DBPath)
			if err != nil {
				return err
			}
			defer db.Close()
			if err := here.Load(db); err != nil {
				return err
			}
			m := here.Member(args[0])
			if m == nil {
				return fmt.Errorf("%q is not a repository in this project (have: %s)",
					args[0], strings.Join(here.MemberIDs(), ", "))
			}
			if err := db.RemoveRepo(args[0]); err != nil {
				return err
			}
			if err := project.RemovePointer(m.CommonDir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"removed %q (%s) from project %s; its claims were released\n"+
					"Its host configuration was left in place — `stigmergy init --remove` there takes it out.\n",
				args[0], m.WorktreeRoot, here.ID)
			return nil
		},
	}
	return cmd
}

func newProjectListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List every project this machine knows about",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			globalPath, err := xdg.GlobalDBPath()
			if err != nil {
				return err
			}
			g, err := store.OpenGlobal(globalPath)
			if err != nil {
				return err
			}
			defer g.Close()

			known, err := g.KnownProjects()
			if err != nil {
				return err
			}
			if len(known) == 0 {
				fmt.Fprintln(out, "No projects are registered yet.")
				fmt.Fprintln(out, "A project registers itself on `stigmergy init`, `stigmergy doctor`,")
				fmt.Fprintln(out, "or the first time an agent opens it.")
				return nil
			}
			for _, kp := range known {
				missing := ""
				if _, err := os.Stat(kp.DBPath); err != nil {
					missing = "  (database missing)"
				}
				label := kp.Label
				var members []store.RepoRow
				if db, err := store.OpenProjectAt(kp.DBPath, store.ReadOnly()); err == nil {
					if n := db.Name(); n != "" {
						label = n
					}
					members, _ = db.Repos()
					db.Close()
				}
				fmt.Fprintf(out, "%-40s %s%s\n", label, kp.DBPath, missing)
				if len(members) > 1 {
					for _, r := range members {
						fmt.Fprintf(out, "    %-22s %s\n", r.RepoID, r.Worktree)
					}
				}
			}
			return nil
		},
	}
	return cmd
}
