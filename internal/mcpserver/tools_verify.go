package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// MemoryVerifyInput records that someone actually checked a memory.
//
// This is the only thing in stigmergy that produces an ASSERTION time. Every
// other timestamp in the memory schema is a mutation time — when bytes last
// changed, for any reason at all. Recording an outcome here is a person or an
// agent saying "I looked, and this is what I found", which is a different claim
// and the only one that can ever support the word "still".
type MemoryVerifyInput struct {
	Key string `json:"key"`
	// Outcome is what you concluded, and it is never inferred for you.
	Outcome string `json:"outcome" jsonschema:"reaffirmed (checked, still true), revised (was wrong, now corrected), or refuted (no longer true)"`
	// ExpectedMemoryVersion is the version you assessed. Required, so an outcome
	// can never be read as applying to text its author never saw.
	ExpectedMemoryVersion int    `json:"expected_memory_version" jsonschema:"the version you actually read and judged"`
	Reason                string `json:"reason,omitempty" jsonschema:"what you checked and how; this is what makes the record useful later"`
}

// MemoryVerifyOutput reports the stored judgement.
type MemoryVerifyOutput struct {
	Verification *store.Verification `json:"verification"`
	Note         string              `json:"note,omitempty"`
}

func (s *Session) memoryVerify(ctx context.Context, _ *mcp.CallToolRequest, in MemoryVerifyInput) (*mcp.CallToolResult, MemoryVerifyOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MemoryVerifyOutput{}, toolError(err)
	}
	if err := store.ValidateOutcome(in.Outcome); err != nil {
		return nil, MemoryVerifyOutput{}, toolError(err)
	}

	rec := store.VerificationRecord{
		Key:                   in.Key,
		Outcome:               in.Outcome,
		ExpectedMemoryVersion: in.ExpectedMemoryVersion,
		Reason:                in.Reason,
		Actor:                 s.actor(),
		AgentKind:             s.agentKind(),
	}

	// Snapshot the evidence as it stands right now, if this memory has a policy.
	//
	// Snapshotted rather than recomputed on read, and that is the whole reason
	// this is worth storing: an outcome is only interpretable against what the
	// observer could actually see at the time. Re-running the measurement later
	// answers a different question, because HEAD has moved and every count with
	// it. Nobody can calibrate "reaffirmed" against a number that has since
	// changed underneath the judgement.
	policy, err := s.project.ReadEvidencePolicy(in.Key)
	switch {
	case err == nil:
		rec.PolicyVersion = &policy.Version
		if info := s.evaluateEvidence(ctx, map[string]*store.EvidencePolicy{in.Key: policy}, []string{in.Key}); info[in.Key] != nil {
			if b, err := json.Marshal(info[in.Key]); err == nil {
				rec.Evidence = string(b)
			}
		}
	case errors.Is(err, store.ErrNoPolicy):
		// No policy is not a failure. Verifying by reading the code, or by
		// asking the user, is a perfectly good way to check something — and a
		// NULL policy version keeps that distinguishable from having had
		// evidence that happened to show nothing.
	default:
		return nil, MemoryVerifyOutput{}, toolError(err)
	}

	v, err := s.project.RecordVerification(rec)
	if err != nil {
		return nil, MemoryVerifyOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryVerifyOutput{Verification: v, Note: verifyNote(in.Outcome, rec.PolicyVersion != nil)}, nil
}

// verifyNote says the one useful thing that follows from each outcome.
//
// The reaffirm case is the one that matters. A reaffirm is the ONLY moment when
// re-capturing an evidence baseline is legitimate — someone has just explicitly
// checked the proposition — and it is exactly the moment an agent will not think
// of it. It stays a separate call rather than happening here: "I checked the bit
// I cared about" is not "I checked everything the policy observes", and only the
// agent knows which of those it just did.
func verifyNote(outcome string, hasPolicy bool) string {
	switch outcome {
	case store.Reaffirmed:
		if hasPolicy {
			return "Recorded. This memory's evidence baseline still points at the old commit, so it will keep reporting every change since then. " +
				"If you checked everything the policy observes — not just the part you came for — recapture it with memory_evidence_set."
		}
		return "Recorded. This is the first kind of timestamp in stigmergy that means \"someone checked\", rather than \"bytes changed\"."
	case store.Revised:
		return "Recorded as a correction rather than a tidy-up — which is a distinction nothing could have inferred from the edit itself."
	case store.Refuted:
		return "Recorded. Nothing was deleted: this says what you found, and what to do about the memory is a separate, deliberate decision."
	}
	return ""
}

// MemoryHistoryInput reads one memory's verification history.
type MemoryHistoryInput struct {
	Key string `json:"key"`
}

// MemoryHistoryOutput is the full sequence, oldest first.
type MemoryHistoryOutput struct {
	Key     string               `json:"key"`
	History []store.Verification `json:"history"`
}

func (s *Session) memoryHistory(_ context.Context, _ *mcp.CallToolRequest, in MemoryHistoryInput) (*mcp.CallToolResult, MemoryHistoryOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpened(); err != nil {
		return nil, MemoryHistoryOutput{}, toolError(err)
	}
	history, err := s.project.VerificationHistory(in.Key)
	if err != nil {
		return nil, MemoryHistoryOutput{}, toolError(err)
	}
	s.touch()
	return nil, MemoryHistoryOutput{Key: in.Key, History: history}, nil
}

// verificationUnsupported rejects the global scope, where there is no
// verification history because there is no table for one.
func verificationUnsupported(scope string) error {
	return serr.E(serr.InvalidInput,
		"verification history is project-only; the %s scope does not keep one", scope)
}
