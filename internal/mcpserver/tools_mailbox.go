package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/happyarch/stigmergy/internal/serr"
	"github.com/happyarch/stigmergy/internal/store"
)

// MailboxSendInput sends mail to another root.
type MailboxSendInput struct {
	ToRoot   string `json:"to_root" jsonschema:"root_id of the agent to write to; claim conflicts name the owner"`
	Subject  string `json:"subject"`
	Body     string `json:"body" jsonschema:"say what you need and what you propose; the other agent has to be able to act on it"`
	ClaimID  *int64 `json:"claim_id,omitempty" jsonschema:"the claim this is about, if any"`
	ThreadID *int64 `json:"thread_id,omitempty" jsonschema:"omit to start a new thread; pass a thread_id to continue one"`
}

// MailboxMessageOutput carries the sent message.
type MailboxMessageOutput struct {
	Message *store.Message `json:"message"`
}

func (s *Session) mailboxSend(_ context.Context, _ *mcp.CallToolRequest, in MailboxSendInput) (*mcp.CallToolResult, MailboxMessageOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxMessageOutput{}, toolError(err)
	}
	msg, err := s.project.SendMessage(store.SendRequest{
		FromRoot: s.actor(), ToRoot: in.ToRoot, Subject: in.Subject, Body: in.Body,
		ClaimID: in.ClaimID, ThreadID: in.ThreadID,
	})
	if err != nil {
		return nil, MailboxMessageOutput{}, toolError(threadErr(err, in.ThreadID))
	}
	s.touch()
	return nil, MailboxMessageOutput{Message: msg}, nil
}

// MailboxInboxInput reads your mail.
type MailboxInboxInput struct {
	UnreadOnly bool `json:"unread_only,omitempty" jsonschema:"true to see only messages you have not read yet"`
}

// MailboxInboxOutput lists messages, newest first.
type MailboxInboxOutput struct {
	Messages []store.Message `json:"messages"`
}

func (s *Session) mailboxInbox(_ context.Context, _ *mcp.CallToolRequest, in MailboxInboxInput) (*mcp.CallToolResult, MailboxInboxOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxInboxOutput{}, toolError(err)
	}
	msgs, err := s.project.Inbox(s.actor(), in.UnreadOnly)
	if err != nil {
		return nil, MailboxInboxOutput{}, toolError(err)
	}
	s.touch()
	return nil, MailboxInboxOutput{Messages: msgs}, nil
}

// MailboxThreadsInput lists the conversations you are part of.
type MailboxThreadsInput struct {
	State string `json:"state,omitempty" jsonschema:"filter by state: open, resolved or abandoned; omit for all"`
}

// MailboxThreadsOutput lists your conversations, most recently active first.
type MailboxThreadsOutput struct {
	Threads []store.ThreadSummary `json:"threads"`
}

// mailboxThreads answers "what conversations am I in?" — which the inbox cannot.
//
// The inbox only shows mail addressed to you, so a message you sent and nobody
// has answered yet appears nowhere. An agent that is compacted mid-negotiation
// would lose the thread id and have no route back to the conversation it started:
// it would re-send, or read the silence as consent and edit anyway.
func (s *Session) mailboxThreads(_ context.Context, _ *mcp.CallToolRequest, in MailboxThreadsInput) (*mcp.CallToolResult, MailboxThreadsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxThreadsOutput{}, toolError(err)
	}
	threads, err := s.project.Threads(s.actor(), in.State)
	if err != nil {
		return nil, MailboxThreadsOutput{}, toolError(err)
	}
	s.touch()
	return nil, MailboxThreadsOutput{Threads: threads}, nil
}

// MailboxMarkReadInput acknowledges messages.
type MailboxMarkReadInput struct {
	MessageIDs []int64 `json:"message_ids"`
}

// MailboxMarkReadOutput reports how many were marked.
type MailboxMarkReadOutput struct {
	MarkedRead int `json:"marked_read"`
}

func (s *Session) mailboxMarkRead(_ context.Context, _ *mcp.CallToolRequest, in MailboxMarkReadInput) (*mcp.CallToolResult, MailboxMarkReadOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxMarkReadOutput{}, toolError(err)
	}
	n, err := s.project.MarkRead(s.actor(), in.MessageIDs)
	if err != nil {
		return nil, MailboxMarkReadOutput{}, toolError(err)
	}
	s.touch()
	return nil, MailboxMarkReadOutput{MarkedRead: n}, nil
}

// MailboxThreadInput reads a whole conversation.
type MailboxThreadInput struct {
	ThreadID int64 `json:"thread_id"`
}

// MailboxThreadOutput carries the thread and its messages.
type MailboxThreadOutput struct {
	Thread *store.Thread `json:"thread"`
}

func (s *Session) mailboxThread(_ context.Context, _ *mcp.CallToolRequest, in MailboxThreadInput) (*mcp.CallToolResult, MailboxThreadOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxThreadOutput{}, toolError(err)
	}
	t, err := s.project.GetThread(in.ThreadID)
	if err != nil {
		return nil, MailboxThreadOutput{}, toolError(threadErr(err, &in.ThreadID))
	}
	s.touch()
	return nil, MailboxThreadOutput{Thread: t}, nil
}

// MailboxResolveInput closes a negotiation.
type MailboxResolveInput struct {
	ThreadID   int64  `json:"thread_id"`
	Resolution string `json:"resolution" jsonschema:"what was agreed; this is the record other agents will read"`
	Abandoned  bool   `json:"abandoned,omitempty" jsonschema:"true if the matter was dropped rather than settled"`
}

func (s *Session) mailboxResolve(_ context.Context, _ *mcp.CallToolRequest, in MailboxResolveInput) (*mcp.CallToolResult, MailboxThreadOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRegistered(); err != nil {
		return nil, MailboxThreadOutput{}, toolError(err)
	}
	state := store.ThreadResolved
	if in.Abandoned {
		state = store.ThreadAbandoned
	}
	t, err := s.project.ResolveThread(in.ThreadID, s.actor(), in.Resolution, state)
	if err != nil {
		return nil, MailboxThreadOutput{}, toolError(threadErr(err, &in.ThreadID))
	}
	s.touch()
	return nil, MailboxThreadOutput{Thread: t}, nil
}

func threadErr(err error, id *int64) error {
	if errors.Is(err, store.ErrNoThread) {
		var n int64
		if id != nil {
			n = *id
		}
		return serr.E(serr.InvalidInput, "there is no thread %d in this project", n)
	}
	return err
}
