package hooks

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/gitx"
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
}

// Guard decides whether an agent may write the given paths.
//
// The order of the checks is the design. Cheap, common outcomes resolve first
// (no repo, no database, no claims), so the overwhelmingly common case — an
// agent editing an unclaimed file — costs one read-only query and nothing else.
// Only genuine uncertainty reaches the fail-closed branch.
func Guard(agentKind, sessionLabel, cwd string, editPaths []string) Decision {
	if len(editPaths) == 0 {
		// An unrecognized tool shape. We do not know what it writes, so we do
		// not pretend to govern it.
		return Decision{Allow: true}
	}

	repo, err := gitx.Resolve(cwd)
	if err != nil {
		// Not a git repository: stigmergy coordinates repositories, and has no
		// opinion about anything else.
		return Decision{Allow: true}
	}

	dbPath := store.ProjectDBPath(repo.CommonDir)
	if _, err := os.Stat(dbPath); err != nil {
		// stigmergy is not enabled here. No database means no claims, means
		// nothing to enforce — an unadopted project must not be slowed or
		// blocked by a hook that happens to be installed globally.
		return Decision{Allow: true}
	}

	db, err := store.OpenProject(repo.CommonDir, store.ReadOnly(), store.BusyTimeout(HookBusyTimeout))
	if err != nil {
		return failClosed("the project database could not be opened", err)
	}
	defer db.Close()

	// A database from a newer stigmergy may express claims in ways this binary
	// cannot read. Silently allowing the edit would mean silently ignoring
	// claims that do exist.
	version, err := db.SchemaVersion()
	if err != nil {
		return failClosed("the project database schema could not be read", err)
	}
	latest, err := store.LatestVersion(store.Project)
	if err != nil || version != latest {
		return failClosed(fmt.Sprintf(
			"the project database is at schema version %d but this stigmergy expects %d", version, latest), err)
	}

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
	for _, p := range editPaths {
		rel, err := paths.Normalize(repo.WorktreeRoot, cwd, p)
		if errors.Is(err, paths.ErrOutsideWorktree) {
			continue // Outside the repo: not governed by claims.
		}
		if err != nil {
			return failClosed(fmt.Sprintf("the path %q could not be resolved", p), err)
		}
		covering, err := db.ClaimsCovering(rel, selfRoot)
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
		fmt.Fprintf(&sb, "  %s is claimed by root %s", c.ScopePath, c.RootID)
		if c.AgentKind != "" {
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
