package hooks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/happyarch/stigmergy/internal/claims"
	"github.com/happyarch/stigmergy/internal/pathglob"
	"github.com/happyarch/stigmergy/internal/store"
)

// Priming implements the end-of-turn doc-rot nudge: docs/association-model.md
// §9. A session's claims are a cue — touching file F is treated as reason to
// resurface memories whose declared evidence covers F, plus one hop of their
// linked neighbors — delivered on the same Stop path that makes mail
// unskippable. Pure SQLite reads throughout, exactly like mail.go: no git, no
// internal/drift (see internal/hooks/imports_test.go).

// PrimedMemory is one memory the note names, with the neighbors that ride
// along with it.
type PrimedMemory struct {
	Key         string
	Description string
	Neighbors   []store.Neighbor
}

// Priming is what CheckPriming found.
type Priming struct {
	// root is the session's own root id: the identity priming_delivered rows
	// are recorded and checked against. Priming rides the Stop hook, which
	// only ever fires for the session's main thread, not for a peer agent —
	// see mail-gate in internal/cli/hook.go, whose shape this mirrors.
	root string
	// Primed is capped at store.MaxPrimingMemories; Overflow counts the rest,
	// so the note can say "...and N more" without naming them.
	Primed   []PrimedMemory
	Overflow int
}

func (p Priming) empty() bool { return len(p.Primed) == 0 }

// CheckPriming finds memories worth surfacing because this session's roots —
// itself and any agent that ran inside it (store.RootsOfSession) — are
// holding claims that overlap what those memories declared as evidence.
// Records nothing; MarkPrimed does that, and only after the text is composed.
func CheckPriming(agentKind, sessionID, cwd string) Priming {
	if sessionID == "" {
		return Priming{}
	}
	db, _, ok := openProject(cwd)
	if !ok {
		return Priming{}
	}
	defer db.Close()

	self, err := db.RootBySession(agentKind, sessionID)
	if err != nil {
		// Not registered: no root of its own, so nothing to key the dedup on.
		return Priming{}
	}

	roots, err := db.RootsOfSession(agentKind, sessionID)
	if err != nil || len(roots) == 0 {
		return Priming{}
	}
	mine := make(map[string]bool, len(roots))
	for _, r := range roots {
		mine[r.RootID] = true
	}

	allClaims, err := db.ActiveClaims("")
	if err != nil {
		return Priming{}
	}
	var myClaims []store.Claim
	for _, c := range allClaims {
		if mine[c.RootID] {
			myClaims = append(myClaims, c)
		}
	}
	if len(myClaims) == 0 {
		return Priming{}
	}

	policies, err := db.EvidencePolicies()
	if err != nil || len(policies) == 0 {
		return Priming{}
	}

	var primedKeys []string
	for key, policy := range policies {
		if primedByAnyClaim(policy, myClaims) {
			primedKeys = append(primedKeys, key)
		}
	}
	if len(primedKeys) == 0 {
		return Priming{}
	}
	sort.Strings(primedKeys)

	already, err := db.PrimingDelivered(self.RootID, primedKeys)
	if err != nil {
		return Priming{}
	}
	fresh := make([]string, 0, len(primedKeys))
	for _, k := range primedKeys {
		if !already[k] {
			fresh = append(fresh, k)
		}
	}
	if len(fresh) == 0 {
		return Priming{}
	}

	shown := fresh
	overflow := 0
	if len(shown) > store.MaxPrimingMemories {
		overflow = len(shown) - store.MaxPrimingMemories
		shown = shown[:store.MaxPrimingMemories]
	}

	neighbors, err := db.NeighborsOf(shown)
	if err != nil {
		neighbors = nil // degrade to a note with no neighbors, not no note
	}

	out := Priming{root: self.RootID, Overflow: overflow}
	for _, k := range shown {
		m, err := db.ReadMemory(k)
		if err != nil {
			continue
		}
		out.Primed = append(out.Primed, PrimedMemory{Key: k, Description: m.Description, Neighbors: neighbors[k]})
	}
	return out
}

// primedByAnyClaim reports whether any of this policy's declared members
// overlaps any of the claims a session's roots are holding.
func primedByAnyClaim(policy *store.EvidencePolicy, myClaims []store.Claim) bool {
	for _, member := range policy.Members {
		for _, c := range myClaims {
			if !sameRepo(c.RepoID, member.RepoID) {
				continue
			}
			// Zero declared paths means whole-member observation — the
			// conservative default (memory-model §4) — which matches any
			// claim in this repository.
			if len(member.Paths) == 0 {
				return true
			}
			scope := claims.Scope{Path: c.ScopePath, Recursive: c.Recursive}
			for _, p := range member.Paths {
				if pathOverlapsClaim(p, scope) {
					return true
				}
			}
		}
	}
	return false
}

// pathOverlapsClaim decides whether one declared evidence path overlaps a
// claimed scope. Literal patterns reuse the claim-overlap algebra directly —
// git's `:(top,literal)` semantics (match the path exactly, or anything
// under it if it names a directory) are exactly what a recursive claims.Scope
// already means. Globs have no file list to test candidates against on the
// hook path, so a non-recursive (single-file) claim is matched directly, and
// a recursive (subtree) claim is tested against the pattern's static
// prefix — the region the pattern could possibly touch. That is a
// conservative approximation, not exact matching, and it is meant to be: a
// missed prime is the failure this whole mechanism exists to prevent (§11).
func pathOverlapsClaim(p store.EvidencePath, scope claims.Scope) bool {
	switch p.Kind {
	case store.PathLiteral:
		return claims.Overlaps(scope, claims.Scope{Path: p.Pattern, Recursive: true})
	case store.PathGlob:
		if !scope.Recursive {
			return pathglob.Match(p.Pattern, scope.Path)
		}
		return claims.Overlaps(scope, claims.Scope{Path: pathglob.StaticPrefix(p.Pattern), Recursive: true})
	default:
		return false
	}
}

// sameRepo mirrors store's unexported rule of the same name: the empty repo
// id is the pre-multi-repo legacy value and matches anything (store.LegacyRepoID).
func sameRepo(a, b string) bool {
	return a == b || a == store.LegacyRepoID || b == store.LegacyRepoID
}

// MarkPrimed records that this session's root has now seen these memories —
// call only AFTER the note text has been composed, mirroring MarkDelivered's
// notified_at rule: marking first would let a crash between mark and render
// eat the note while the record says it was shown.
//
// Unlike MarkDelivered, this takes no agent kind or session id: CheckPriming
// already resolved the session's root into p.root, so there is no identity
// left to look up — only the database to write to.
func MarkPrimed(cwd string, p Priming) {
	if p.empty() || p.root == "" {
		return
	}
	db, _, ok := openProject(cwd)
	if !ok {
		return
	}
	defer db.Close()
	keys := make([]string, 0, len(p.Primed))
	for _, m := range p.Primed {
		keys = append(keys, m.Key)
	}
	_ = db.MarkPrimed(p.root, keys)
}

// PrimingText renders a priming note exactly as an agent will read it.
func PrimingText(p Priming) string {
	if p.empty() {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "stigmergy: %s primed by files you touched.\n\n", plural(len(p.Primed), "memory", "memories"))
	for _, m := range p.Primed {
		fmt.Fprintf(&sb, "  %s — %s\n", m.Key, m.Description)
		for _, n := range m.Neighbors {
			fmt.Fprintf(&sb, "    ↳ %s — %s (%s)\n", n.Key, n.Description, n.Reason)
		}
	}
	if p.Overflow > 0 {
		fmt.Fprintf(&sb, "  ...and %s (memory_list)\n", plural(p.Overflow, "more", "more"))
	}
	sb.WriteString("\nYou changed files these memories are about. Check the cross-referenced " +
		"docs/code before finishing; memory_verify what you confirmed, update or unlink what you invalidated.\n")
	return sb.String()
}
