package store

import (
	"fmt"
	"strings"
)

// ProbeFTS5 verifies the linked SQLite build ships the FTS5 extension. The
// whole search feature depends on it, so this runs in doctor and in tests.
func (d *DB) ProbeFTS5() error {
	if _, err := d.Exec(`CREATE VIRTUAL TABLE temp.fts5_probe USING fts5(x)`); err != nil {
		return fmt.Errorf("store: SQLite build lacks FTS5: %w", err)
	}
	_, err := d.Exec(`DROP TABLE temp.fts5_probe`)
	return err
}

// FTSQuery converts free text into a safe FTS5 MATCH expression by quoting
// each token, so user queries can never hit FTS syntax errors. Tokens are
// implicitly ANDed: search is precise.
func FTSQuery(query string) string {
	return strings.Join(quoteTokens(query), " ")
}

// FTSQueryAny is FTSQuery with the tokens ORed, for similarity suggestions
// where a partial match is the point.
func FTSQueryAny(query string) string {
	return strings.Join(quoteTokens(query), " OR ")
}

func quoteTokens(query string) []string {
	fields := strings.Fields(query)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return quoted
}
