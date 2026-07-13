package hooks

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/happyarch/stigmergy/internal/gitx"
	"github.com/happyarch/stigmergy/internal/store"
)

// auditBudget caps how long a hook will spend recording what it did. The
// decision is already made by then; an audit write that cannot finish quickly
// is dropped rather than allowed to add latency to the agent's edit.
const auditBudget = 100 * time.Millisecond

// SessionStartText is injected into an agent's context when a session begins.
//
// It returns "" for projects that have not adopted stigmergy: a hook installed
// user-wide fires in every repository, and injecting instructions for a tool
// that is not in use there would be noise at best.
//
// The session id matters more than it looks. It is how the claim guard tells
// the agent's own claims from everyone else's; without it every agent would be
// blocked by its own claims, so it is stated as the first thing to do.
func SessionStartText(agentKind, sessionID, cwd string) string {
	repo, err := gitx.Resolve(cwd)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(store.ProjectDBPath(repo.CommonDir)); err != nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("This project uses stigmergy for shared memory and coordination between agents.\n\n")
	fmt.Fprintf(&sb, "Register now, before your first edit:\n"+
		"  1. context_open(project_root=%q)\n"+
		"  2. root_register(agent_kind=%q, worktree=%q, session_label=%q)\n\n",
		repo.WorktreeRoot, agentKind, repo.WorktreeRoot, sessionID)
	sb.WriteString("Use session_label exactly as given: it is how stigmergy knows which claims are yours. " +
		"Until you register, every claim in the repository — including any you made earlier — will block your edits.\n\n")
	sb.WriteString("Then: memory_search before starting work, and claim_acquire before editing files others might touch. " +
		"Edits to files claimed by another agent are blocked.\n")

	if claims := activeClaimSummary(repo); claims != "" {
		sb.WriteString("\nClaims currently held by other agents:\n")
		sb.WriteString(claims)
	}
	return sb.String()
}

// activeClaimSummary tells an arriving agent what is already spoken for, so it
// can plan around the contested files instead of discovering them one blocked
// edit at a time.
func activeClaimSummary(repo *gitx.Repo) string {
	db, err := store.OpenProject(repo.CommonDir, store.ReadOnly(), store.BusyTimeout(HookBusyTimeout))
	if err != nil {
		return ""
	}
	defer db.Close()

	active, err := db.ActiveClaims("")
	if err != nil || len(active) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range active {
		scope := c.ScopePath
		if c.Recursive {
			scope += "/**"
		}
		fmt.Fprintf(&sb, "  %s — %s (%s)\n", scope, c.Reason, c.RootID)
	}
	return sb.String()
}

// EndSession ends the host session's root so its claims are freed immediately.
// Everything here is best-effort: the session is already over, and the root TTL
// is the backstop if this does not land.
func EndSession(agentKind, sessionID, cwd string) {
	repo, err := gitx.Resolve(cwd)
	if err != nil {
		return
	}
	if _, err := os.Stat(store.ProjectDBPath(repo.CommonDir)); err != nil {
		return
	}
	db, err := store.OpenProject(repo.CommonDir, store.BusyTimeout(HookBusyTimeout))
	if err != nil {
		return
	}
	defer db.Close()

	root, err := db.RootBySession(agentKind, sessionID)
	if err != nil {
		return
	}
	_ = db.DeregisterRoot(root.RootID)
}

// AuditCodexConflict records an edit that landed on a claimed path.
//
// This is the audit entry that matters most in the whole system: it is the only
// record that a claim was actually violated, rather than merely enforced. On
// Claude such an edit is impossible; on Codex it is merely halted afterwards,
// so the trail is what tells an operator which file to go and check.
func AuditCodexConflict(sessionID, cwd string, d Decision) {
	repo, err := gitx.Resolve(cwd)
	if err != nil {
		return
	}
	db, err := store.OpenProject(repo.CommonDir, store.BusyTimeout(HookBusyTimeout))
	if err != nil {
		return
	}
	defer db.Close()

	for _, c := range d.Conflicts {
		_ = db.Audit(store.AuditEntry{
			Actor:     sessionID,
			AgentKind: "codex",
			Action:    "codex_post_edit_conflict",
			Target:    c.ScopePath,
			Detail: fmt.Sprintf("edit landed on a path claimed by %s (claim %d, reason: %s); turn halted",
				c.RootID, c.ID, c.Reason),
		})
	}
}

// AuditDenial records a blocked edit. It runs after the decision, never before:
// a failure to write the audit log must not change what the agent is allowed to
// do, and must not make it wait.
func AuditDenial(agentKind, sessionID, cwd string, d Decision) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		repo, err := gitx.Resolve(cwd)
		if err != nil {
			return
		}
		db, err := store.OpenProject(repo.CommonDir, store.BusyTimeout(HookBusyTimeout))
		if err != nil {
			return
		}
		defer db.Close()

		target, detail := "", "claim verification unavailable"
		if len(d.Conflicts) > 0 {
			target = d.Conflicts[0].ScopePath
			detail = fmt.Sprintf("blocked by claim %d held by %s", d.Conflicts[0].ID, d.Conflicts[0].RootID)
		}
		_ = db.Audit(store.AuditEntry{
			Actor:     sessionID,
			AgentKind: agentKind,
			Action:    "edit_blocked",
			Target:    target,
			Detail:    detail,
		})
	}()

	select {
	case <-done:
	case <-time.After(auditBudget):
		// Over budget. The denial already stands; the log entry is not worth
		// making the agent wait for.
	}
}
