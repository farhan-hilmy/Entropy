package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"entropy/internal/config"
)

// Experiment is an experiment from entropy.yaml at its current version.
// Entropy numbers the versions: a sync that finds the definition changed adds one.
type Experiment struct {
	Name       string            `json:"name"`
	Version    int               `json:"version"`
	Definition config.Experiment `json:"definition"`
	// Missing means it was removed from entropy.yaml; its runs are kept.
	Missing bool `json:"missing"`
}

// Run is one execution of an experiment. Snapshot is exactly what ran.
type Run struct {
	ID         string            `json:"id"`
	Experiment string            `json:"experiment"`
	Version    int               `json:"version"`
	RerunOf    string            `json:"rerunOf,omitempty"` // the run this one repeats exactly
	Snapshot   json.RawMessage   `json:"snapshot,omitempty"`
	State      string            `json:"state"`
	Verdict    string            `json:"verdict,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	EndedAt    time.Time         `json:"endedAt,omitzero"`
	Events     []RunEvent        `json:"events,omitempty"`
	Assertions []AssertionResult `json:"assertions,omitempty"`
}

// RunEvent is one line of a run's timeline. Type is phase, fault, trigger,
// response or system.
type RunEvent struct {
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// AssertionResult is how one of the experiment's assertions came out;
// Index is its position in the experiment. Outcome: pass, fail, not_evaluated.
type AssertionResult struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

func syncExperiments(ctx context.Context, tx *sql.Tx, exps []config.Experiment, sum *Summary) error {
	old := map[string]Experiment{}
	hashes := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT name, version, hash, missing FROM experiment`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e Experiment
		var hash string
		if err := rows.Scan(&e.Name, &e.Version, &hash, &e.Missing); err != nil {
			rows.Close()
			return err
		}
		old[e.Name], hashes[e.Name] = e, hash
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, e := range exps {
		def, err := json.Marshal(e)
		if err != nil {
			return err
		}
		h := sha256.Sum256(def)
		hash := hex.EncodeToString(h[:])
		prev, ok := old[e.Name]
		switch {
		case !ok:
			if _, err := tx.ExecContext(ctx, `INSERT INTO experiment (name, version, hash, definition) VALUES (?, 1, ?, ?)`,
				e.Name, hash, string(def)); err != nil {
				return err
			}
			sum.Added = append(sum.Added, "experiment "+e.Name)
		case hashes[e.Name] != hash:
			if _, err := tx.ExecContext(ctx, `UPDATE experiment SET version = ?, hash = ?, definition = ?, missing = 0 WHERE name = ?`,
				prev.Version+1, hash, string(def), e.Name); err != nil {
				return err
			}
			sum.Updated = append(sum.Updated, "experiment "+e.Name)
		case prev.Missing:
			if _, err := tx.ExecContext(ctx, `UPDATE experiment SET missing = 0 WHERE name = ?`, e.Name); err != nil {
				return err
			}
			sum.Updated = append(sum.Updated, "experiment "+e.Name)
		}
		delete(old, e.Name)
	}
	for name, prev := range old {
		if prev.Missing {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE experiment SET missing = 1 WHERE name = ?`, name); err != nil {
			return err
		}
		sum.Missing = append(sum.Missing, "experiment "+name)
	}
	return nil
}

func (s *Store) Experiments(ctx context.Context) ([]Experiment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, version, definition, missing FROM experiment ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Experiment{}
	for rows.Next() {
		var e Experiment
		var def string
		if err := rows.Scan(&e.Name, &e.Version, &def, &e.Missing); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(def), &e.Definition); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	if r.ID == "" {
		r.ID = newID()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run (id, experiment, version, rerun_of, snapshot, state, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Experiment, r.Version, r.RerunOf, string(r.Snapshot), r.State, formatTime(r.StartedAt))
	return err
}

// SetRunState records the run's state, and its verdict and reason once it has them.
func (s *Store) SetRunState(ctx context.Context, id, state, verdict, reason string, endedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run SET state = ?, verdict = ?, reason = ?, ended_at = ? WHERE id = ?`,
		state, verdict, reason, formatTime(endedAt), id)
	return err
}

func (s *Store) AddRunEvent(ctx context.Context, runID string, e RunEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_event (run_id, at, type, message, data) VALUES (?, ?, ?, ?, ?)`,
		runID, formatTime(e.At), e.Type, e.Message, string(e.Data))
	return err
}

func (s *Store) SaveAssertionResults(ctx context.Context, runID string, results []AssertionResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range results {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO assertion_result (run_id, idx, outcome, detail) VALUES (?, ?, ?, ?)`,
			runID, r.Index, r.Outcome, r.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Run returns one run with its snapshot, timeline and assertion results.
func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	runs, err := s.queryRuns(ctx, true, `WHERE id = ?`, id)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, sql.ErrNoRows
	}
	r := runs[0]
	rows, err := s.db.QueryContext(ctx, `SELECT at, type, message, data FROM run_event WHERE run_id = ? ORDER BY id`, id)
	if err != nil {
		return r, err
	}
	r.Events = []RunEvent{}
	for rows.Next() {
		var e RunEvent
		var at, data string
		if err := rows.Scan(&at, &e.Type, &e.Message, &data); err != nil {
			rows.Close()
			return r, err
		}
		e.At = parseTime(at)
		if data != "" {
			e.Data = json.RawMessage(data)
		}
		r.Events = append(r.Events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return r, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT idx, outcome, detail FROM assertion_result WHERE run_id = ? ORDER BY idx`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Assertions = []AssertionResult{}
	for rows.Next() {
		var a AssertionResult
		if err := rows.Scan(&a.Index, &a.Outcome, &a.Detail); err != nil {
			return r, err
		}
		r.Assertions = append(r.Assertions, a)
	}
	return r, rows.Err()
}

// Runs returns the latest runs, newest first, without snapshots or timelines.
// experiment narrows them to one experiment when not empty.
func (s *Store) Runs(ctx context.Context, experiment string, limit int) ([]Run, error) {
	if experiment != "" {
		return s.queryRuns(ctx, false, `WHERE experiment = ? ORDER BY started_at DESC LIMIT ?`, experiment, limit)
	}
	return s.queryRuns(ctx, false, `ORDER BY started_at DESC LIMIT ?`, limit)
}

// EndUnfinishedRuns marks runs still in progress as aborted: Entropy stopped
// during them (its startup removes their faults).
func (s *Store) EndUnfinishedRuns(ctx context.Context, finished []string, reason string) (int64, error) {
	args := []any{reason, formatTime(time.Now())}
	q := `UPDATE run SET state = 'done', verdict = 'aborted', reason = ?, ended_at = ? WHERE state NOT IN (`
	for i, st := range finished {
		if i > 0 {
			q += ", "
		}
		q += "?"
		args = append(args, st)
	}
	res, err := s.db.ExecContext(ctx, q+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) queryRuns(ctx context.Context, withSnapshot bool, where string, args ...any) ([]Run, error) {
	snapshot := `''`
	if withSnapshot {
		snapshot = `snapshot`
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, experiment, version, rerun_of, `+snapshot+`, state, verdict, reason, started_at, ended_at FROM run `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var snapshot, started, ended string
		if err := rows.Scan(&r.ID, &r.Experiment, &r.Version, &r.RerunOf, &snapshot, &r.State, &r.Verdict, &r.Reason, &started, &ended); err != nil {
			return nil, err
		}
		if snapshot != "" {
			r.Snapshot = json.RawMessage(snapshot)
		}
		r.StartedAt, r.EndedAt = parseTime(started), parseTime(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
