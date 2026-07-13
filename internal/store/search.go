package store

import "github.com/happyarch/stigmergy/internal/serr"

// Scopes is a set of memory scopes, in the order an agent asked for them.
type Scopes []Kind

// DefaultScopes is what memory_search uses when the caller names none.
var DefaultScopes = Scopes{Project, Global}

// ParseScopes validates scope names coming off the wire.
func ParseScopes(names []string) (Scopes, error) {
	if len(names) == 0 {
		return DefaultScopes, nil
	}
	out := make(Scopes, 0, len(names))
	seen := map[Kind]bool{}
	for _, n := range names {
		k := Kind(n)
		if k != Project && k != Global {
			return nil, serr.E(serr.InvalidInput, "scope %q is invalid: must be \"project\" or \"global\"", n)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// SearchScopes runs the query against each requested scope and concatenates the
// results with project always ahead of global, regardless of the order asked
// for. Project knowledge is the more specific answer, and agents act on what
// they read first. Ranking stays bm25 within each scope, capped per scope, so
// a noisy global DB can never crowd out project hits.
func SearchScopes(project, global *DB, query string, scopes Scopes) ([]SearchHit, error) {
	hits := []SearchHit{}
	for _, k := range []Kind{Project, Global} {
		if !scopes.has(k) {
			continue
		}
		db := project
		if k == Global {
			db = global
		}
		if db == nil {
			continue
		}
		found, err := db.SearchMemories(query)
		if err != nil {
			return nil, err
		}
		hits = append(hits, found...)
	}
	return hits, nil
}

func (s Scopes) has(k Kind) bool {
	for _, v := range s {
		if v == k {
			return true
		}
	}
	return false
}
