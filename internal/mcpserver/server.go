package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP server name. Hosts derive tool names from it
// (mcp__stigmergy__*), and the Claude hooks match on that prefix, so it is part
// of the contract — not a display string.
const ServerName = "stigmergy"

// New builds the MCP server and its session. The session is returned so the
// caller can end the root on shutdown.
func New(version, globalPath string) (*mcp.Server, *Session) {
	s := NewSession(globalPath)

	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "stigmergy", Version: version},
		&mcp.ServerOptions{Instructions: Instructions()},
	)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_open",
		Description: "Open the stigmergy databases for a git repository. Call this first, with the absolute path of your working directory.",
	}, s.contextOpen)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "root_register",
		Description: "Register (or resume) this session as a root: the agent that may claim files and write memory. Pass your host session id as session_label so your own claims never block you.",
	}, s.rootRegister)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "root_list_active",
		Description: "List the agents actually working in this repository right now, what each holds, and how recently each was heard from. " +
			"Consult this before you write to anyone: a root id you remembered from earlier may belong to an agent that has since died.",
	}, s.rootListActive)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "root_heartbeat",
		Description: "Refresh this root's liveness so its claims keep holding. Any tool call also does this.",
	}, s.rootHeartbeat)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "root_deregister",
		Description: "End this root and release its claims. Call it when your work is done.",
	}, s.rootDeregister)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_search",
		Description: "Search shared memories. Do this before starting work and before writing a new memory. Project hits are listed before global ones.",
	}, s.memorySearch)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_read",
		Description: "Read one memory's full body.",
	}, s.memoryRead)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_list",
		Description: "List the keys and descriptions in a scope, without bodies.",
	}, s.memoryList)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_write",
		Description: "Create or update a memory under compare-and-swap. Omit expected_version to create; pass the current version to update. On a conflict the current entry is returned: merge and retry.",
	}, s.memoryWrite)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_promote",
		Description: "Copy a project memory into the global scope, for facts that hold in every repository.",
	}, s.memoryPromote)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_link",
		Description: "Associate two memories in the same scope, with the reason the association matters. Links are shown to every agent that reads or finds either memory. " +
			"Record project-specific connections your general knowledge cannot infer.",
	}, s.memoryLink)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_unlink",
		Description: "Remove an association between two memories.",
	}, s.memoryUnlink)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_evidence_set",
		Description: "Declare which repositories and paths to observe for changes when assessing a memory, and capture the commits to compare from. " +
			"This records where to look; it is not a claim about what the memory means, and calling it verifies nothing.",
	}, s.memoryEvidenceSet)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_evidence_clear",
		Description: "Remove a memory's evidence policy. Its accumulated baselines go with it.",
	}, s.memoryEvidenceClear)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_verify",
		Description: "Record that you actually checked a memory, and what you concluded: reaffirmed, revised, or refuted. " +
			"This is the only thing that records when a memory was last CHECKED, as opposed to when it last changed. It is never inferred from an edit.",
	}, s.memoryVerify)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_history",
		Description: "Read one memory's verification history: who checked it, when, what they concluded, and the evidence they saw.",
	}, s.memoryHistory)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_delete",
		Description: "Permanently delete a memory. Requires its current version, and is audited.",
	}, s.memoryDelete)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "episode_record",
		Description: "Record what happened: a session, an investigation, a failure and why. History, not truth — immutable once written. " +
			"Optionally ground one or more memories (they cite this as where the lesson came from) and chain onto an earlier episode you are correcting or continuing.",
	}, s.episodeRecord)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "episode_read",
		Description: "Read one episode: its body, the memories it grounds, and — always — the chain of later episodes that correct or continue it.",
	}, s.episodeRead)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "episode_list",
		Description: "List episodes, most recent first, optionally filtered by a full-text query or a time window. The search surface for episodes; memory_search stays memories-only.",
	}, s.episodeList)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_acquire",
		Description: "Reserve a file or directory before editing it, so no other agent edits it at the same time. Take the narrowest scope that covers your work, and release it when you are done.",
	}, s.claimAcquire)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_check",
		Description: "Check whether a path is claimed, and by whom, before you edit it.",
	}, s.claimCheck)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_list_active",
		Description: "List every claim currently in force in this repository.",
	}, s.claimListActive)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_renew",
		Description: "Extend one of your claims because you are still working on it.",
	}, s.claimRenew)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_release",
		Description: "Release one of your claims. Do this as soon as you are done: someone may be waiting.",
	}, s.claimRelease)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_send",
		Description: "Write to another root — use this when a claim blocks you, rather than waiting silently or editing around it. Pass a thread_id to continue an existing conversation.",
	}, s.mailboxSend)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_inbox",
		Description: "Read mail addressed to you. Check it when you are blocked, and answer promptly when another agent is blocked on you.",
	}, s.mailboxInbox)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_threads",
		Description: "List the conversations you are part of, including ones where you are waiting for an answer. Your inbox does not show messages you sent, so check here before assuming silence means consent.",
	}, s.mailboxThreads)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_thread",
		Description: "Read a whole conversation, including every message in it.",
	}, s.mailboxThread)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_mark_read",
		Description: "Acknowledge messages you have read, so the sender knows they landed.",
	}, s.mailboxMarkRead)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mailbox_resolve",
		Description: "Close a negotiation and record what was agreed.",
	}, s.mailboxResolve)

	return srv, s
}

// A note on what is deliberately NOT here: an unread-mail notice appended to
// every tool result.
//
// It is the obvious way to make delivery host-agnostic, and it was tried. It
// cannot be done without breaking a contract this server keeps everywhere else:
// a tool result's text IS its JSON body, exactly, so an agent can parse it
// instead of reading prose and guessing. Appending "you have 2 unread messages"
// to the end of that leaves a payload that is no longer JSON, and buys redundancy
// at the price of the one property that makes every other tool dependable.
//
// Mail is delivered by the hooks instead — Stop on Claude, UserPromptSubmit and
// PostToolUse on Codex — which reach the agent without touching what the tools
// return. See internal/hooks/mail.go.

// Run serves MCP over stdio until the host closes the connection, then ends the
// root so its claims do not linger for the full TTL.
func Run(ctx context.Context, version, globalPath string) error {
	srv, session := New(version, globalPath)
	defer session.Close()
	defer session.EndRoot()
	// If the host supplied its session in the environment, register before
	// serving, so the agent's first claim_acquire works without a prior handshake.
	session.bootstrapFromEnv()
	return srv.Run(ctx, &mcp.StdioTransport{})
}
