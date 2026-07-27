package hooks

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/paths"
	"github.com/happyarch/stigmergy/internal/store"
)

// HookBusyTimeout is how long the hook waits for a database lock. It is far
// below the server's timeout on purpose: the agent is blocked while we decide,
// so a contended database must surface as a fast failure, not a stall.
const HookBusyTimeout = 250

// Decision is what a guard concluded about an attempted edit.
type Decision struct {
	Allow  bool
	Reason string
	// Conflicts are the foreign claims covering the path, if any.
	Conflicts []store.Claim
	// CWD is the working directory whose project produced this decision. It
	// matters for the hosts that can edit across two projects at once: a caller
	// recording the denial has to write it to the database that actually holds
	// the claim, not to whichever workspace the tool call happened to list first.
	CWD string
}

// Guard decides whether an agent may write the given paths.
//
// The order of the checks is the design. Cheap, common outcomes resolve first
// (no repo, no database, no claims), so the overwhelmingly common case — an
// agent editing an unclaimed file — costs one read-only query and nothing else.
// Only genuine uncertainty reaches the fail-closed branch.
func Guard(agentKind, sessionLabel, cwd string, editPaths []string) Decision {
	d := guard(agentKind, sessionLabel, cwd, editPaths)
	d.CWD = cwd
	return d
}

func guard(agentKind, sessionLabel, cwd string, editPaths []string) Decision {
	if len(editPaths) == 0 {
		// An unrecognized tool shape. We do not know what it writes, so we do
		// not pretend to govern it.
		return Decision{Allow: true}
	}

	// The guard is the one hook that acts on the difference between "nothing to
	// govern here" and "something is here and I cannot verify it": the first
	// allows, the second fails closed. See resolveProject.
	p, why := resolveProject(cwd, readOnly)
	if why != nil {
		return failClosed(why.Reason, why.Err)
	}
	if p == nil {
		return Decision{Allow: true}
	}
	db := p.DB
	defer db.Close()

	// Resolve who we are. An unregistered session has no root, so every claim
	// is foreign to it — including, potentially, one it made in a previous
	// life. That is the intended nudge: register, and your own claims stop
	// blocking you.
	selfRoot := ""
	if root, err := db.RootBySession(agentKind, sessionLabel); err == nil {
		selfRoot = root.RootID
	} else if !errors.Is(err, store.ErrNoRoot) {
		return failClosed("the claim owner could not be resolved", err)
	}

	var conflicts []store.Claim
	for _, edit := range editPaths {
		// cwd found the PROJECT. Which repository governs this edit is decided by
		// the path itself, not by where the agent happens to be standing — that
		// is the whole change here. Every host can edit outside its cwd's
		// repository (Claude's --add-dir, Antigravity's multiple workspaces, an
		// absolute path from anywhere), and resolving against cwd's worktree meant
		// those edits landed outside it, were treated as ungoverned, and were
		// silently ALLOWED past another agent's claim.
		abs, err := absolutize(cwd, edit)
		if err != nil {
			return failClosed(fmt.Sprintf("the path %q could not be resolved", edit), err)
		}
		member := p.Project.Containing(abs)
		if member == nil {
			// Containing is a string comparison and knows nothing about symlinks,
			// so a path that reaches into this project under another name looks
			// like it is outside it — and "outside" means allow. A link in /tmp
			// pointing at a claimed file was therefore writable while the file
			// itself was blocked, which is a fail-OPEN in the one component whose
			// entire job is to fail closed.
			//
			// Resolving is only paid for when the cheap placement finds nothing.
			// For an agent editing inside its own project that is never; for one
			// writing to /tmp it is a couple of Lstat calls, which is affordable
			// on a path this hot precisely because it is the rare branch.
			if resolved, rerr := paths.Resolve(abs); rerr == nil && resolved != abs {
				member = p.Project.Containing(resolved)
			}
		}
		if member == nil {
			continue // Outside every repository of this project: not ours to govern.
		}
		rel, err := paths.Normalize(member.WorktreeRoot, cwd, edit)
		if errors.Is(err, paths.ErrOutsideWorktree) {
			// Containing said otherwise, so the two disagree — a symlink out of
			// the tree, most likely. Normalize is the stricter of the two and it
			// wins; it is the one that resolves symlinks.
			continue
		}
		if err != nil {
			return failClosed(fmt.Sprintf("the path %q could not be resolved", edit), err)
		}
		covering, err := db.ClaimsCovering(member.ID, rel, selfRoot)
		if err != nil {
			return failClosed("claims could not be read", err)
		}
		for _, c := range covering {
			if !c.Own {
				conflicts = append(conflicts, c)
			}
		}
	}
	if len(conflicts) == 0 {
		return Decision{Allow: true}
	}
	return Decision{Allow: false, Reason: denyReason(conflicts), Conflicts: conflicts}
}

// GuardWorkspaces decides for a set of edits that may be spread across several
// mounted workspaces, and therefore across several unrelated projects.
//
// Guard already handles many repositories, but only within ONE project: it
// resolves a project from a single cwd and then uses that project's roster to
// place each path. Antigravity is the host that can break that assumption —
// it reports every workspace the user has open, and there is nothing stopping
// two of them being different projects with different databases.
//
// So group the edits by the workspace that contains each one, and guard each
// group against its own project. In the ordinary case every edit lands in one
// group and this is exactly Guard with one extra map lookup.
//
// cwdFor maps an edit path to the workspace holding it; the host supplies it,
// because only the host knows what it mounted.
func GuardWorkspaces(agentKind, sessionLabel string, cwdFor func(string) string, editPaths []string) Decision {
	if len(editPaths) == 0 {
		return Decision{Allow: true}
	}
	groups, order := groupByWorkspace(cwdFor, editPaths)

	var conflicts []store.Claim
	deciding := ""
	for _, cwd := range order {
		d := Guard(agentKind, sessionLabel, cwd, groups[cwd])
		if d.Allow {
			continue
		}
		if deciding == "" {
			// The first workspace to deny owns the decision, so a caller auditing
			// it opens the project that holds the claim it names.
			deciding = cwd
		}
		// A fail-closed group has no conflicts to report and its own reason
		// already says what to do, so it short-circuits: there is nothing to
		// usefully merge it with.
		if len(d.Conflicts) == 0 {
			return d
		}
		conflicts = append(conflicts, d.Conflicts...)
	}
	if len(conflicts) == 0 {
		return Decision{Allow: true}
	}
	return Decision{Allow: false, Reason: denyReason(conflicts), Conflicts: conflicts, CWD: deciding}
}

// Workspaces is the distinct workspaces holding the given edits, in the order
// they first appear. It is what a host needs for the things that are per-project
// rather than per-call — a heartbeat is owed to every project being edited in,
// not only to the one that happened to be listed first.
func Workspaces(cwdFor func(string) string, editPaths []string) []string {
	_, order := groupByWorkspace(cwdFor, editPaths)
	return order
}

// groupByWorkspace buckets edits by the workspace containing each, keeping first
// appearance order so decisions and messages are stable.
func groupByWorkspace(cwdFor func(string) string, editPaths []string) (map[string][]string, []string) {
	groups := map[string][]string{}
	order := []string{}
	for _, p := range editPaths {
		cwd := cwdFor(p)
		if _, seen := groups[cwd]; !seen {
			order = append(order, cwd)
		}
		groups[cwd] = append(groups[cwd], p)
	}
	return groups, order
}

// absolutize resolves an agent-supplied path against the working directory,
// without touching the filesystem.
//
// Deliberately cheap and deliberately not symlink-resolving: this only has to be
// good enough to pick which repository a path belongs to. paths.Normalize does
// the careful work — symlinks, missing files, escape checks — once the member is
// known, and it is the authority if the two ever disagree.
func absolutize(cwd, p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if cwd == "" {
		return "", errors.New("hooks: relative path with no working directory to resolve it against")
	}
	return filepath.Join(cwd, p), nil
}

// failClosed is the answer when we cannot tell whether a path is claimed.
//
// Allowing would mean overwriting another agent's work whenever the database is
// unavailable — precisely when things are already going wrong. So we block, and
// say plainly what to do about it: this is recoverable, and the agent must not
// mistake it for a claim conflict to negotiate.
func failClosed(what string, err error) Decision {
	detail := ""
	if err != nil {
		detail = ": " + err.Error()
	}
	return Decision{
		Allow: false,
		Reason: fmt.Sprintf(
			"stigmergy: claim verification is unavailable (%s%s), so this edit is blocked rather than risk overwriting another agent's work. Run `stigmergy doctor` to diagnose; this is not a claim conflict and there is no one to negotiate with.",
			what, detail),
	}
}

// denyReason tells the agent who holds the path, why, and what to do — an agent
// that is only told "no" will retry, work around, or give up.
func denyReason(conflicts []store.Claim) string {
	return "stigmergy: this edit is blocked by an active claim.\n" + ConflictDetail(conflicts) +
		"Report this to your root. It can negotiate with mailbox_send(to_root, subject, body), " +
		"wait for the claim to expire, or work on something else. Do not edit around the claim."
}

// ConflictDetail describes who holds the conflicting claims and why.
//
// It is deliberately separate from the surrounding advice: what to do about a
// claim differs by host — Claude blocks the edit outright, Codex cannot — and a
// message that tells a Codex agent its edit was "blocked" would teach it a
// model of the system that is simply false.
func ConflictDetail(conflicts []store.Claim) string {
	var sb strings.Builder
	for _, c := range conflicts {
		// Qualified, not the bare scope path: in a project spanning several
		// repositories "src/api is claimed" does not tell an agent enough to act
		// on, and it names a path that exists in more than one of them.
		fmt.Fprintf(&sb, "  %s is claimed by root %s", c.Qualified(), c.RootID)
		// Who you are about to argue with, as specifically as we can say it:
		// the harness, and the model behind it if it told us.
		switch {
		case c.AgentKind != "" && c.OwnerModel != "":
			fmt.Fprintf(&sb, " (%s, %s)", c.AgentKind, c.OwnerModel)
		case c.AgentKind != "":
			fmt.Fprintf(&sb, " (%s)", c.AgentKind)
		}
		fmt.Fprintf(&sb, "\n    reason:   %s\n    worktree: %s\n", c.Reason, c.Worktree)
		if c.Branch != "" {
			fmt.Fprintf(&sb, "    branch:   %s\n", c.Branch)
		}
		fmt.Fprintf(&sb, "    expires:  %s\n", humanExpiry(c.ExpiresAt))
		// Who to talk to, spelled out. An agent reconstructing a root id from
		// memory or from an old message is how mail ends up addressed to an agent
		// that died an hour ago, so the id it needs is put in front of it at the
		// exact moment it needs it, together with whether that root can answer.
		fmt.Fprintf(&sb, "    owner:    %s — write to it with mailbox_send(to_root=%q)\n",
			c.OwnerLiveness, c.RootID)
	}
	return sb.String()
}

// humanExpiry renders an expiry as a duration, because "in 12 minutes" tells an
// agent whether waiting is a plan and a timestamp does not.
func humanExpiry(expiresAt string) string {
	t, err := store.ParseStamp(expiresAt)
	if err != nil {
		return expiresAt
	}
	d := t.Sub(store.NowTime()).Round(time.Second)
	if d <= 0 {
		return "now"
	}
	return fmt.Sprintf("%s (in %s)", expiresAt, d)
}
