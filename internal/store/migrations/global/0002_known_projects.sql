-- Every project database this machine has seen, so one command can reach all of
-- them.
--
-- This exists because of a specific, repeated incident. The hook path fails
-- closed when a project database's schema version differs from the version
-- compiled into the binary, while the MCP server migrates on open and does not
-- version-gate. So the moment a binary carrying a new migration lands on PATH,
-- EVERY adopted project on the machine blocks all edits — including the one
-- being worked in — until `stigmergy doctor` is run in each of them, one by one,
-- from inside each repository. Nothing in the repository you are standing in
-- tells you the others exist, and the agents working there have no idea why they
-- suddenly cannot edit anything.
--
-- A registry turns that scavenger hunt into `stigmergy doctor --all`. It is
-- deliberately a record of projects that ANNOUNCED themselves — `init`, `doctor`,
-- and opening a project over MCP all write here — and never the result of
-- scanning the filesystem for databases. A tool that went looking for projects
-- would eventually find one it should not have touched.
--
-- Keyed on db_path rather than on a git common dir because the project database
-- does not always live in one: a project spanning several repositories keeps its
-- database outside all of them. The path is the one identifier both shapes have.
CREATE TABLE known_projects (
  db_path    TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  last_seen  TEXT NOT NULL
);
