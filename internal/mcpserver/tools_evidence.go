package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/drift"
	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// EvidenceBudget caps a whole drift evaluation, however many repositories it
// spans. Derived from the request context, so a client that gives up first ends
// it sooner.
//
// It is a budget for the phase and not per member: members run concurrently, so
// one wedged repository cannot spend everyone else's time, and the ones that did
// answer are still reported under partial coverage.
const EvidenceBudget = 15 * time.Second

// EvidenceInfo is the pinned response shape for change evidence.
//
// Configured and Coverage are INDEPENDENT axes and are reported separately on
// purpose. "This memory declares nothing to observe" and "what it declares could
// not be observed this time" are different facts, and an earlier draft that
// collapsed them into one field could not express the difference — which is how
// "2 of 3 measured" ends up read with the confidence of 3 of 3.
type EvidenceInfo struct {
	Configured bool `json:"configured"`
	// State is "not_configured" or "evaluated". Never "fresh", never "stale":
	// this system does not hold an opinion about whether a memory is still true.
	State         string               `json:"state"`
	PolicyVersion int                  `json:"policy_version,omitempty"`
	MeasuredAt    string               `json:"measured_at,omitempty"`
	Coverage      string               `json:"coverage,omitempty"`
	Members       []drift.MemberResult `json:"members,omitempty"`
}

// notConfigured is what a memory with no policy reports when drift was asked
// for.
//
// It is a value and never an omitted field. Omission is indistinguishable from
// "the caller never asked for drift", which is precisely the ambiguity the flag
// exists to remove. Coverage is meaningless without a policy and stays absent.
func notConfigured() *EvidenceInfo {
	return &EvidenceInfo{Configured: false, State: "not_configured"}
}

// MemoryEvidenceSetInput declares where to look when assessing a memory.
type MemoryEvidenceSetInput struct {
	Key string `json:"key"`
	// ExpectedMemoryVersion is always required, even when creating a policy: a
	// baseline attached to a proposition you have not read is measuring
	// something you never saw.
	ExpectedMemoryVersion int `json:"expected_memory_version" jsonschema:"the memory's current version"`
	// ExpectedPolicyVersion is omitted only when no policy exists yet.
	ExpectedPolicyVersion *int `json:"expected_policy_version,omitempty" jsonschema:"omit if there is no policy yet; pass the current policy version to replace one"`
	// Repos may be omitted in a single-repository project, where there is
	// exactly one possible answer.
	Repos []EvidenceRepoInput `json:"repos,omitempty" jsonschema:"repositories to observe; omit in a single-repository project"`
}

// EvidenceRepoInput is one declared repository and its optional path narrowing.
type EvidenceRepoInput struct {
	Repo string `json:"repo" jsonschema:"a repository of this project, as context_open lists it"`
	// Paths narrow observation within this repository. Declaring NONE observes
	// the whole repository, which is the conservative default: a too-narrow
	// anchor undercounts silently and reads as plausibly clean.
	Paths []EvidencePathInput `json:"paths,omitempty" jsonschema:"optional; no paths means the whole repository"`
}

// EvidencePathInput is one pattern.
type EvidencePathInput struct {
	Kind    string `json:"kind" jsonschema:"literal or glob"`
	Pattern string `json:"pattern" jsonschema:"repo-relative; ** crosses directory separators, * does not"`
}

// MemoryEvidenceSetOutput reports the stored policy.
type MemoryEvidenceSetOutput struct {
	Policy *store.EvidencePolicy `json:"policy"`
	Note   string                `json:"note,omitempty"`
}

func (s *Session) memoryEvidenceSet(ctx context.Context, _ *mcp.CallToolRequest, in MemoryEvidenceSetInput) (*mcp.CallToolResult, MemoryEvidenceSetOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryEvidenceSetOutput{}, toolError(err)
	}

	repos, err := s.resolveEvidenceRepos(in.Repos)
	if err != nil {
		return nil, MemoryEvidenceSetOutput{}, toolError(err)
	}

	// Capture every baseline BEFORE opening a transaction, and fail the whole
	// operation if any one of them cannot be resolved. Storing a policy whose
	// baselines are partly missing would look identical to one that is simply
	// quiet.
	captureCtx, cancel := context.WithTimeout(ctx, EvidenceBudget)
	defer cancel()
	members := make([]store.EvidenceMember, 0, len(repos))
	for _, r := range repos {
		head, err := drift.CaptureHead(captureCtx, r.worktree)
		if err != nil {
			return nil, MemoryEvidenceSetOutput{}, toolError(serr.E(serr.InvalidInput,
				"the baseline for %q could not be captured, so nothing was stored: %v", r.repoID, err))
		}
		paths := make([]store.EvidencePath, 0, len(r.paths))
		for _, p := range r.paths {
			paths = append(paths, store.EvidencePath{Kind: p.Kind, Pattern: p.Pattern})
		}
		members = append(members, store.EvidenceMember{RepoID: r.repoID, BaseOID: head, Paths: paths})
	}

	policy, err := s.project.SetEvidencePolicy(store.EvidenceSet{
		Key:                   in.Key,
		ExpectedMemoryVersion: in.ExpectedMemoryVersion,
		ExpectedPolicyVersion: in.ExpectedPolicyVersion,
		Members:               members,
		Actor:                 s.actor(),
		AgentKind:             s.agentKind(),
	})
	if err != nil {
		return nil, MemoryEvidenceSetOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryEvidenceSetOutput{
		Policy: policy,
		Note: "Baselines captured. From now on this reports what has CHANGED in the declared scope — " +
			"never whether the memory is still true. Nobody has verified anything by calling this.",
	}, nil
}

// MemoryEvidenceClearInput removes a policy.
type MemoryEvidenceClearInput struct {
	Key                   string `json:"key"`
	ExpectedMemoryVersion int    `json:"expected_memory_version" jsonschema:"the memory's current version"`
	ExpectedPolicyVersion int    `json:"expected_policy_version" jsonschema:"the policy's current version"`
}

// MemoryEvidenceClearOutput reports the removal.
type MemoryEvidenceClearOutput struct {
	Cleared bool   `json:"cleared"`
	Key     string `json:"key"`
}

func (s *Session) memoryEvidenceClear(_ context.Context, _ *mcp.CallToolRequest, in MemoryEvidenceClearInput) (*mcp.CallToolResult, MemoryEvidenceClearOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryEvidenceClearOutput{}, toolError(err)
	}
	err := s.project.ClearEvidencePolicy(in.Key, in.ExpectedMemoryVersion, in.ExpectedPolicyVersion,
		s.actor(), s.agentKind())
	if errors.Is(err, store.ErrNoPolicy) {
		return nil, MemoryEvidenceClearOutput{}, toolError(serr.E(serr.InvalidInput,
			"memory %q has no evidence policy to clear", in.Key))
	}
	if err != nil {
		return nil, MemoryEvidenceClearOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryEvidenceClearOutput{Cleared: true, Key: in.Key}, nil
}

// resolvedRepo is one validated declaration, with its worktree found.
type resolvedRepo struct {
	repoID   string
	worktree string
	paths    []EvidencePathInput
}

// resolveEvidenceRepos maps declared repository names onto this project's
// members.
//
// Omitting the list is allowed only where it cannot be a guess: a project with
// exactly one repository has exactly one possible answer, the same reasoning
// that lets a bare claim scope mean "the repository you opened". With two or
// more, the list is required and the error names them — picking one would be
// choosing which of several real answers the agent meant.
func (s *Session) resolveEvidenceRepos(in []EvidenceRepoInput) ([]resolvedRepo, error) {
	if s.proj == nil {
		return nil, serr.E(serr.Internal, "no project is open")
	}
	if err := s.proj.Load(s.project); err != nil {
		return nil, serr.Internalf(err, "the project's repositories could not be read")
	}
	members := s.proj.Members
	if len(members) == 0 {
		return nil, serr.E(serr.InvalidInput,
			"this project has no registered repositories, so there is nothing to observe — run `stigmergy doctor` here, which records them")
	}

	if len(in) == 0 {
		if len(members) > 1 {
			return nil, serr.E(serr.InvalidInput,
				"this project spans %d repositories (%s), so which to observe cannot be inferred — name them",
				len(members), joinIDs(s.proj.MemberIDs()))
		}
		return []resolvedRepo{{repoID: members[0].ID, worktree: members[0].WorktreeRoot}}, nil
	}

	out := make([]resolvedRepo, 0, len(in))
	for _, r := range in {
		m := s.proj.Member(r.Repo)
		if m == nil {
			return nil, serr.E(serr.InvalidInput,
				"%q is not a repository in this project (%s)", r.Repo, joinIDs(s.proj.MemberIDs()))
		}
		out = append(out, resolvedRepo{repoID: m.ID, worktree: m.WorktreeRoot, paths: r.Paths})
	}
	return out, nil
}

func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	out := ids[0]
	for _, id := range ids[1:] {
		out += ", " + id
	}
	return out
}

// evaluateEvidence runs the drift phase for a set of policies.
//
// Returns nil when there are no policies at all, so the common case — a project
// where nobody has declared any evidence — costs one map lookup and no
// subprocesses.
func (s *Session) evaluateEvidence(ctx context.Context, policies map[string]*store.EvidencePolicy, keys []string) map[string]*EvidenceInfo {
	out := map[string]*EvidenceInfo{}
	if len(policies) == 0 {
		return out
	}
	if err := s.proj.Load(s.project); err != nil {
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, EvidenceBudget)
	defer cancel()

	measuredAt := store.Now()
	for _, key := range keys {
		p, ok := policies[key]
		if !ok {
			continue
		}
		members := make([]drift.Member, 0, len(p.Members))
		for _, m := range p.Members {
			d := drift.Member{RepoID: m.RepoID, BaseOID: m.BaseOID}
			// A member whose repository has since been removed from the project
			// still has its declaration reported, with nowhere to look — which is
			// the honest answer, and visibly different from zero changes.
			if pm := s.proj.Member(m.RepoID); pm != nil {
				d.Worktree, d.CommonDir = pm.WorktreeRoot, pm.CommonDir
			}
			for _, path := range m.Paths {
				d.Paths = append(d.Paths, drift.Path{Kind: path.Kind, Pattern: path.Pattern})
			}
			members = append(members, d)
		}
		res := drift.Evaluate(ctx, members)
		out[key] = &EvidenceInfo{
			Configured:    true,
			State:         "evaluated",
			PolicyVersion: p.Version,
			MeasuredAt:    measuredAt,
			Coverage:      res.Coverage,
			Members:       res.Members,
		}
	}
	return out
}

// evidenceUnsupported rejects drift where it has no meaning, rather than
// accepting the flag and quietly returning nothing.
func evidenceUnsupported(scope string) error {
	return serr.E(serr.InvalidInput,
		"change evidence is project-only: the %s scope has no repository to observe, and a memory about this machine has no git history that bears on it",
		scope)
}

// baselineNote is what memory_write says after an edit to a memory that has a
// policy.
//
// The baseline is deliberately NOT re-captured here. Re-capturing on a write
// would mean a typo fix silently erased every commit of evidence accumulated
// since — inferring "the agent re-verified this" from the fact that bytes
// changed. Keeping the old baseline over-reports change, which is visible and
// arguable; resetting it under-reports, which is invisible.
//
// Only emitted when a policy exists. On every write in the system it would be
// noise, and noise is what agents learn to skip past.
func baselineNote(key string) string {
	return fmt.Sprintf(
		"Evidence baseline unchanged for %q. If this edit reasserts the proposition, recapture explicitly with memory_evidence_set; if it merely fixes wording, leave it alone.",
		key)
}
