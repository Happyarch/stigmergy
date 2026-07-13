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
		Name:        "memory_delete",
		Description: "Permanently delete a memory. Requires its current version, and is audited.",
	}, s.memoryDelete)

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

// Run serves MCP over stdio until the host closes the connection, then ends the
// root so its claims do not linger for the full TTL.
func Run(ctx context.Context, version, globalPath string) error {
	srv, session := New(version, globalPath)
	defer session.Close()
	defer session.EndRoot()
	return srv.Run(ctx, &mcp.StdioTransport{})
}
