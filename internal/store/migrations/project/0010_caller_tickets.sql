-- Which agent inside one host session made a tool call.
--
-- The problem this solves is that a host session is not an agent. Claude Code
-- runs several agents — a main thread and its peers — inside ONE process, and
-- they all share ONE MCP server connection. The server therefore sees every
-- call arriving on the same connection, from the same session id, and cannot
-- tell them apart. Before this table it had exactly one answer for all of them:
-- the session's root. Peers either shared an identity they never agreed to, or
-- were refused outright, which is where this started.
--
-- The host does know who is calling. It says so twice, in two channels that had
-- never been joined:
--
--   PreToolUse hook payload   {"agent_id": "a4d39a33457e0df63",
--                              "agent_type": "general-purpose",
--                              "tool_use_id": "toolu_01WCt9...", ...}
--   MCP tools/call params     {"_meta": {"claudecode/toolUseId": "toolu_01WCt9..."}}
--
-- Neither is documented; both were captured from a live 2.1.220 session. The
-- hook fires immediately before the call it describes, and the tool-use id is
-- minted by the host and appears verbatim in both. So the hook writes a ticket
-- here, the server reads it back by that id, and the call is attributed to the
-- agent that actually made it.
--
-- Why this is enforcement and not an honour system: an agent never handles the
-- tool-use id, cannot choose it, and cannot write a ticket — only the hook does,
-- and the hook is run by the host, not by the model. An agent that lies about
-- who it is changes nothing here. That is the whole reason for the indirection;
-- an "which agent are you" tool argument would have been one line and worth
-- nothing.
--
-- Absence is meaningful and safe: no ticket means either the main thread (whose
-- payload carries no agent_id) or a host that installs no hooks, and both fall
-- back to the session root, which is exactly the behaviour that existed before.

CREATE TABLE caller_tickets (
  -- The host's own tool-use id. Primary key because it identifies one call, and
  -- a call is claimed exactly once: the server deletes the ticket as it reads it.
  tool_use_id   TEXT PRIMARY KEY,
  -- The composed label of the agent that made the call — "<session>#<type>:<id>"
  -- — not the bare session id. It is written by whoever composes it (see
  -- store.AgentLabel) so the hook and the server can never disagree about the
  -- spelling of an identity.
  session_label TEXT NOT NULL,
  agent_kind    TEXT NOT NULL,
  -- Where the agent was standing. A peer can be in a different worktree of the
  -- same project than the session that spawned it, and its root should say so.
  worktree      TEXT NOT NULL,
  created_at    TEXT NOT NULL
);

-- Tickets are consumed within milliseconds; the ones left behind are calls the
-- hook stamped and the server never saw (denied at the permission prompt,
-- interrupted, a host that changed its mind). This index serves the sweep that
-- collects them, which is the only reason anything ever scans this table.
CREATE INDEX idx_caller_tickets_age ON caller_tickets(created_at);
