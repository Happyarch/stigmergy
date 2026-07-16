-- Record which model an agent is, alongside which host it is running in.
--
-- agent_kind answers "what harness" — claude-code, codex, antigravity — and
-- nothing about who is actually behind it. Two roots reading "claude-code" may
-- be an Opus and a Haiku, which is most of what an operator wants to know when
-- reading the roster or deciding who to hand a file to.
--
-- The model is self-reported and unvalidated on purpose: nothing in a hook
-- payload or an MCP session carries it, so the only source is the agent's own
-- answer. It is therefore advisory — roots are identified by root_id, and
-- nothing keys off this column. A wrong value is cosmetic, which is what makes
-- asking acceptable.
--
-- Nullable, with no CHECK: model names are somebody else's release schedule.
-- A closed set here would need a migration every time a vendor ships, and
-- would reject the true answer in the meantime. ADD COLUMN also leaves the
-- table and its indexes alone, unlike the rebuild 0003 needed.

ALTER TABLE roots ADD COLUMN model TEXT;
