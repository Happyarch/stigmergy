package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

// AuditEntry is one row of the append-only audit log. Actor is a root_id (or
// "importer"); AgentKind mirrors the root's kind when known.
type AuditEntry struct {
	Actor     string
	AgentKind string
	Action    string
	Target    string
	Detail    string
}

// execer is satisfied by both *sql.DB and *sql.Tx, so a mutation can write its
// audit row inside the same transaction that made the change.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func audit(x execer, e AuditEntry) error {
	_, err := x.Exec(
		`INSERT INTO audit_log(at, actor, agent_kind, action, target, detail)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		Now(), nullStr(e.Actor), nullStr(e.AgentKind), e.Action, nullStr(e.Target), nullStr(e.Detail),
	)
	return err
}

// Audit appends a row outside of any transaction. Mutations should prefer the
// in-transaction path so the log can never disagree with the data.
func (d *DB) Audit(e AuditEntry) error { return audit(d.DB, e) }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// BodyHash is the audit fingerprint for destructive operations: it lets an
// operator confirm what was deleted without the log retaining the content.
func BodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
