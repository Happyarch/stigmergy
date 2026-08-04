package store

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/happyarch/stigmergy/internal/serr"
)

// Episodes are history, not state (docs/association-model.md §4): what
// happened, actor- and time-bound, true forever as a record even if the
// reasoning it captured is later found wrong. IMMUTABLE — there is
// deliberately no UpdateEpisode here, ever; a wrong episode is corrected by a
// NEW episode chained on top with 'corrects', never rewritten in place.
// Project scope only: episodes are session-bound, and the global database has
// no roots or sessions to bind them to.

// Correction/continuation kinds for episode_links.
const (
	EpisodeCorrects  = "corrects"
	EpisodeContinues = "continues"
)

// MaxEpisodeListLimit caps how many episodes one episode_list call returns.
const (
	DefaultEpisodeListLimit = 20
	MaxEpisodeListLimit     = 50
)

// Episode is one immutable record.
type Episode struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Actor     string `json:"actor"`
	AgentKind string `json:"agent_kind"`
	At        string `json:"at"`
}

// EpisodeMemoryNote is one memory an episode grounds, and why.
type EpisodeMemoryNote struct {
	Key  string `json:"key"`
	Note string `json:"note"`
}

// EpisodeSuccessor is a later episode that corrects or continues one earlier
// episode — one hop of the chain from the earlier episode's point of view.
type EpisodeSuccessor struct {
	EpisodeID int64  `json:"episode_id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	At        string `json:"at"`
}

// EpisodeCitation is the memory_read-side view: which episode grounds this
// memory, and when.
type EpisodeCitation struct {
	EpisodeID int64  `json:"episode_id"`
	Title     string `json:"title"`
	At        string `json:"at"`
}

// EpisodeDetail is what episode_read returns: the episode itself, the
// memories it grounds, and its successor chain — surfaced unconditionally,
// because superseded reasoning must never be read without its correction
// (docs/association-model.md §4, the §5 push principle applied to episodes).
type EpisodeDetail struct {
	Episode
	Grounds    []EpisodeMemoryNote `json:"grounds"`
	Successors []EpisodeSuccessor  `json:"successors"`
}

// RecordEpisode is a record/ground/chain call: one episode, optionally
// grounding a set of memories (all under the same note) and optionally
// chaining onto an earlier one it corrects or continues.
type RecordEpisode struct {
	Title              string
	Body               string
	Actor              string
	AgentKind          string
	MemoryKeys         []string
	Note               string
	CorrectsEpisodeID  *int64
	ContinuesEpisodeID *int64
}

func (r RecordEpisode) validate() error {
	if err := ValidateLine("title", r.Title, MaxLineLength); err != nil {
		return err
	}
	if strings.TrimSpace(r.Title) == "" {
		return serr.E(serr.InvalidInput, "title must not be empty: it is what episode_list shows without reading the body")
	}
	if err := ValidateBlock("body", r.Body); err != nil {
		return err
	}
	if strings.TrimSpace(r.Body) == "" {
		return serr.E(serr.InvalidInput, "body must not be empty")
	}
	if r.Actor == "" {
		return serr.E(serr.InvalidInput, "actor must be set")
	}
	if len(r.MemoryKeys) > 0 {
		if err := ValidateLine("note", r.Note, MaxLineLength); err != nil {
			return err
		}
		if strings.TrimSpace(r.Note) == "" {
			return serr.E(serr.InvalidInput,
				"note must not be empty when grounding memories: it is what a future reader sees as the citation")
		}
	}
	if r.CorrectsEpisodeID != nil && r.ContinuesEpisodeID != nil && *r.CorrectsEpisodeID == *r.ContinuesEpisodeID {
		return serr.E(serr.InvalidInput, "an episode cannot both correct and continue the same earlier episode")
	}
	seen := map[string]bool{}
	for _, k := range r.MemoryKeys {
		if err := ValidateKey(k); err != nil {
			return err
		}
		if seen[k] {
			return serr.E(serr.InvalidInput, "memory %q is named twice", k)
		}
		seen[k] = true
	}
	return nil
}

// RecordEpisode inserts one episode, grounds it in zero or more memories, and
// chains it onto an earlier episode it corrects or continues — one
// transaction, so a partially-recorded episode is never visible.
func (d *DB) RecordEpisode(r RecordEpisode) (*Episode, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, serr.Internalf(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	for _, k := range r.MemoryKeys {
		if _, err := readMemoryTx(tx, k); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, serr.E(serr.InvalidInput, "memory %q does not exist, so this episode cannot ground it", k)
			}
			return nil, err
		}
	}
	for _, ref := range []*int64{r.CorrectsEpisodeID, r.ContinuesEpisodeID} {
		if ref == nil {
			continue
		}
		if _, err := readEpisodeTx(tx, *ref); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, serr.E(serr.InvalidInput, "episode %d does not exist, so this episode cannot chain onto it", *ref)
			}
			return nil, err
		}
	}

	now := Now()
	res, err := tx.Exec(
		`INSERT INTO episodes(title, body, actor, agent_kind, at) VALUES(?, ?, ?, ?, ?)`,
		r.Title, r.Body, r.Actor, r.AgentKind, now,
	)
	if err != nil {
		return nil, serr.Internalf(err, "failed to record the episode")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, serr.Internalf(err, "failed to read the new episode's id")
	}

	for _, k := range r.MemoryKeys {
		if _, err := tx.Exec(
			`INSERT INTO episode_memory(episode_id, key, note) VALUES(?, ?, ?)`,
			id, k, r.Note,
		); err != nil {
			return nil, serr.Internalf(err, "failed to ground the episode in %q", k)
		}
	}
	if r.CorrectsEpisodeID != nil {
		if err := insertEpisodeLinkTx(tx, *r.CorrectsEpisodeID, id, EpisodeCorrects); err != nil {
			return nil, err
		}
	}
	if r.ContinuesEpisodeID != nil {
		if err := insertEpisodeLinkTx(tx, *r.ContinuesEpisodeID, id, EpisodeContinues); err != nil {
			return nil, err
		}
	}

	if err := audit(tx, AuditEntry{
		Actor: r.Actor, AgentKind: r.AgentKind, Action: "episode_record",
		Target: string(d.Kind) + ":episode:" + strconv.FormatInt(id, 10),
		Detail: r.Title,
	}); err != nil {
		return nil, serr.Internalf(err, "failed to write audit record")
	}
	if err := tx.Commit(); err != nil {
		return nil, serr.Internalf(err, "failed to commit the episode")
	}
	return &Episode{ID: id, Title: r.Title, Body: r.Body, Actor: r.Actor, AgentKind: r.AgentKind, At: now}, nil
}

func insertEpisodeLinkTx(tx *sql.Tx, episodeID, successorID int64, kind string) error {
	if _, err := tx.Exec(
		`INSERT INTO episode_links(episode_id, successor_id, kind) VALUES(?, ?, ?)`,
		episodeID, successorID, kind,
	); err != nil {
		return serr.Internalf(err, "failed to chain episode %d onto %d", successorID, episodeID)
	}
	return nil
}

const episodeCols = `id, title, body, actor, agent_kind, at`

func scanEpisode(row interface{ Scan(...any) error }) (*Episode, error) {
	var e Episode
	err := row.Scan(&e.ID, &e.Title, &e.Body, &e.Actor, &e.AgentKind, &e.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, serr.Internalf(err, "failed to read episode")
	}
	if err := canonicalStamps(strconv.FormatInt(e.ID, 10), &e.At); err != nil {
		return nil, err
	}
	return &e, nil
}

func readEpisodeTx(tx *sql.Tx, id int64) (*Episode, error) {
	return scanEpisode(tx.QueryRow(`SELECT `+episodeCols+` FROM episodes WHERE id = ?`, id))
}

// ReadEpisode returns one episode's body, provenance and successor chain, or
// ErrNotFound.
func (d *DB) ReadEpisode(id int64) (*EpisodeDetail, error) {
	ep, err := scanEpisode(d.QueryRow(`SELECT `+episodeCols+` FROM episodes WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	grounds, err := d.episodeProvenance(id)
	if err != nil {
		return nil, err
	}
	successors, err := d.episodeSuccessors(id)
	if err != nil {
		return nil, err
	}
	return &EpisodeDetail{Episode: *ep, Grounds: grounds, Successors: successors}, nil
}

func (d *DB) episodeProvenance(id int64) ([]EpisodeMemoryNote, error) {
	rows, err := d.Query(`SELECT key, note FROM episode_memory WHERE episode_id = ? ORDER BY key`, id)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read episode provenance")
	}
	defer rows.Close()
	out := []EpisodeMemoryNote{}
	for rows.Next() {
		var n EpisodeMemoryNote
		if err := rows.Scan(&n.Key, &n.Note); err != nil {
			return nil, serr.Internalf(err, "failed to read a provenance row")
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// episodeSuccessors walks the correction/continuation chain forward from id,
// breadth-first, so a multiply-corrected episode ("X corrected by Y,
// corrected again by Z") surfaces the whole path rather than only the first
// hop — reading X must not leave the impression that Y is still current once
// Z has superseded it too. Dedup guards against a cycle no CHECK constraint
// forbids beyond the immediate self-link.
func (d *DB) episodeSuccessors(id int64) ([]EpisodeSuccessor, error) {
	out := []EpisodeSuccessor{}
	visited := map[int64]bool{id: true}
	queue := []int64{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rows, err := d.Query(
			`SELECT el.successor_id, e.title, el.kind, e.at
			   FROM episode_links el JOIN episodes e ON e.id = el.successor_id
			  WHERE el.episode_id = ?`, cur)
		if err != nil {
			return nil, serr.Internalf(err, "failed to read the successor chain")
		}
		var found []EpisodeSuccessor
		for rows.Next() {
			var s EpisodeSuccessor
			if err := rows.Scan(&s.EpisodeID, &s.Title, &s.Kind, &s.At); err != nil {
				rows.Close()
				return nil, serr.Internalf(err, "failed to read a successor")
			}
			found = append(found, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, serr.Internalf(err, "failed to read the successor chain")
		}
		for _, s := range found {
			if err := canonicalStamps(strconv.FormatInt(s.EpisodeID, 10), &s.At); err != nil {
				return nil, err
			}
			if visited[s.EpisodeID] {
				continue
			}
			visited[s.EpisodeID] = true
			out = append(out, s)
			queue = append(queue, s.EpisodeID)
		}
	}
	slices.SortFunc(out, func(a, b EpisodeSuccessor) int { return cmp.Compare(a.EpisodeID, b.EpisodeID) })
	return out, nil
}

// EpisodeQuery selects episode_list's results: recent-first, optionally
// filtered by an FTS query and/or a time window. Both bounds are inclusive.
type EpisodeQuery struct {
	Since  string
	Before string
	Query  string
	Limit  int
}

func (q EpisodeQuery) normalize() (EpisodeQuery, error) {
	out := q
	for _, b := range []struct {
		name string
		in   string
		out  *string
	}{
		{"since", q.Since, &out.Since},
		{"before", q.Before, &out.Before},
	} {
		if b.in == "" {
			continue
		}
		c, err := CanonicalStamp(b.in)
		if err != nil {
			return out, serr.E(serr.InvalidInput,
				"%s %q is not a timestamp: use RFC3339, e.g. \"2026-07-01T00:00:00Z\"", b.name, b.in)
		}
		*b.out = c
	}
	if out.Since != "" && out.Before != "" && out.Since > out.Before {
		return out, serr.E(serr.InvalidInput,
			"since (%s) is after before (%s), so nothing can match", q.Since, q.Before)
	}
	switch {
	case out.Limit <= 0:
		out.Limit = DefaultEpisodeListLimit
	case out.Limit > MaxEpisodeListLimit:
		out.Limit = MaxEpisodeListLimit
	}
	return out, nil
}

// ListEpisodes returns episodes recent-first, the episodic search surface —
// memory_search deliberately never gained this (docs/association-model.md
// §10.2): an extra result class interacting with MaxSearchHits would truncate
// differently than an agent expects.
//
// Filtering and sorting happen in Go, the same rule QueryMemories follows and
// for the same reason: SQLite would compare non-canonical timestamps as raw
// text before Go ever got the chance to canonicalise them.
func (d *DB) ListEpisodes(q EpisodeQuery) ([]Episode, error) {
	q, err := q.normalize()
	if err != nil {
		return nil, err
	}

	var rows *sql.Rows
	if q.Query != "" {
		match := FTSQuery(q.Query)
		if match == "" {
			return []Episode{}, nil
		}
		rows, err = d.Query(
			`SELECT e.id, e.title, e.body, e.actor, e.agent_kind, e.at
			   FROM episodes_fts f JOIN episodes e ON e.id = f.rowid
			  WHERE episodes_fts MATCH ?`, match)
		if err != nil {
			return nil, serr.E(serr.UnsupportedSearch, "episode search query could not be evaluated: %v", err).Wrap(err)
		}
	} else {
		rows, err = d.Query(`SELECT ` + episodeCols + ` FROM episodes`)
		if err != nil {
			return nil, serr.Internalf(err, "failed to list episodes")
		}
	}
	defer rows.Close()

	all := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read episodes")
	}

	if q.Since != "" || q.Before != "" {
		kept := all[:0]
		for _, e := range all {
			if q.Since != "" && e.At < q.Since {
				continue
			}
			if q.Before != "" && e.At > q.Before {
				continue
			}
			kept = append(kept, e)
		}
		all = kept
	}

	slices.SortFunc(all, func(a, b Episode) int {
		if c := cmp.Compare(b.At, a.At); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	if len(all) > q.Limit {
		all = all[:q.Limit]
	}
	return all, nil
}

// EpisodeProvenanceFor batches provenance lookup for memory_read: the latest
// citations for each of the given keys, plus each key's true total — the
// same "always present, capped, total reported when truncated" shape links
// use, not a coincidence: it is the one push-exposure contract this design
// applies everywhere (docs/association-model.md §5).
const MaxProvenanceSurfaced = 5

// EpisodeProvenanceEntry is one memory's citations, capped.
type EpisodeProvenanceEntry struct {
	Citations []EpisodeCitation
	Total     int
}

func (d *DB) EpisodeProvenanceFor(keys []string) (map[string]EpisodeProvenanceEntry, error) {
	out := map[string]EpisodeProvenanceEntry{}
	keys = dedupeKeys(keys)
	if len(keys) == 0 {
		return out, nil
	}
	ph := placeholders(len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := d.Query(
		`SELECT em.key, e.id, e.title, e.at
		   FROM episode_memory em JOIN episodes e ON e.id = em.episode_id
		  WHERE em.key IN (`+ph+`)`, args...)
	if err != nil {
		return nil, serr.Internalf(err, "failed to read episode provenance")
	}
	defer rows.Close()

	byKey := map[string][]EpisodeCitation{}
	for rows.Next() {
		var key string
		var c EpisodeCitation
		if err := rows.Scan(&key, &c.EpisodeID, &c.Title, &c.At); err != nil {
			return nil, serr.Internalf(err, "failed to read a provenance row")
		}
		if err := canonicalStamps(strconv.FormatInt(c.EpisodeID, 10), &c.At); err != nil {
			return nil, err
		}
		byKey[key] = append(byKey[key], c)
	}
	if err := rows.Err(); err != nil {
		return nil, serr.Internalf(err, "failed to read episode provenance")
	}

	for key, cites := range byKey {
		slices.SortFunc(cites, func(a, b EpisodeCitation) int { return cmp.Compare(b.At, a.At) })
		total := len(cites)
		if len(cites) > MaxProvenanceSurfaced {
			cites = cites[:MaxProvenanceSurfaced]
		}
		out[key] = EpisodeProvenanceEntry{Citations: cites, Total: total}
	}
	return out, nil
}
