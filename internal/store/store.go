// Package store keeps Entropy's records in a single SQLite file.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	_ "modernc.org/sqlite"

	"entropy/internal/config"
)

// The MVP has exactly one environment, so environment is a single row and the
// other tables don't carry an environment_id yet.
const schema = `
CREATE TABLE IF NOT EXISTS environment (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	name        TEXT NOT NULL,
	config_path TEXT NOT NULL,
	synced_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS dependency (
	id       TEXT PRIMARY KEY,
	name     TEXT NOT NULL UNIQUE,
	kind     TEXT NOT NULL,
	address  TEXT NOT NULL,
	upstream TEXT NOT NULL,
	listen   TEXT NOT NULL,
	redirect INTEGER NOT NULL DEFAULT 0,
	missing  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS service (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL UNIQUE,
	base_url         TEXT NOT NULL,
	health_check     TEXT NOT NULL DEFAULT '',
	health_command   TEXT NOT NULL DEFAULT '',
	health_every_sec INTEGER NOT NULL DEFAULT 0,
	missing          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS service_dependency (
	service_id    TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
	dependency_id TEXT NOT NULL REFERENCES dependency(id) ON DELETE CASCADE,
	PRIMARY KEY (service_id, dependency_id)
);
CREATE TABLE IF NOT EXISTS command (
	service_id  TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	run         TEXT NOT NULL,
	cwd         TEXT NOT NULL,
	timeout_sec INTEGER NOT NULL,
	PRIMARY KEY (service_id, name)
);
-- Keyed by names, not ids, so history survives a command being edited or removed.
CREATE TABLE IF NOT EXISTS command_run (
	id          TEXT PRIMARY KEY,
	service     TEXT NOT NULL,
	command     TEXT NOT NULL,
	run         TEXT NOT NULL,
	started_at  TEXT NOT NULL,
	duration_ms INTEGER NOT NULL,
	exit_code   INTEGER NOT NULL,
	timed_out   INTEGER NOT NULL,
	error       TEXT NOT NULL,
	output      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS command_run_recent ON command_run (service, command, started_at);
`

// addedColumns were added after their table was first created. CREATE TABLE IF
// NOT EXISTS doesn't change an existing table, so existing databases get them here.
var addedColumns = []struct{ table, column, definition string }{
	{"service", "health_command", "TEXT NOT NULL DEFAULT ''"},
	{"service", "health_every_sec", "INTEGER NOT NULL DEFAULT 0"},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.column + " " + c.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

type Store struct {
	db *sql.DB
}

type Environment struct {
	Name       string    `json:"name"`
	ConfigPath string    `json:"configPath"`
	SyncedAt   time.Time `json:"syncedAt"`
}

type Dependency struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Upstream string `json:"upstream"`
	Listen   string `json:"listen"`
	Redirect bool   `json:"redirect"`
	// Missing means the dependency was removed from entropy.yaml. The record is
	// kept so experiments that target it can say so instead of breaking.
	Missing bool `json:"missing"`
}

type Service struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	BaseURL        string    `json:"baseUrl"`
	HealthCheck    string    `json:"healthCheck"`
	HealthCommand  string    `json:"healthCommand"`
	HealthEverySec int       `json:"healthEverySec"`
	Uses           []string  `json:"uses"`
	Commands       []Command `json:"commands"`
	Missing        bool      `json:"missing"`
}

type Command struct {
	Name       string `json:"name"`
	Run        string `json:"run"`
	Cwd        string `json:"cwd"`
	TimeoutSec int    `json:"timeoutSec"`
}

// CommandRun is one execution of a command. Output is the tail of stdout and stderr.
type CommandRun struct {
	ID         string    `json:"id"`
	Service    string    `json:"service"`
	Command    string    `json:"command"`
	Run        string    `json:"run"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	ExitCode   int       `json:"exitCode"`
	TimedOut   bool      `json:"timedOut"`
	Error      string    `json:"error,omitempty"`
	Output     string    `json:"output,omitempty"`
}

// Summary lists what a sync changed, e.g. "dependency redis".
type Summary struct {
	Added   []string `json:"added"`
	Updated []string `json:"updated"`
	Missing []string `json:"missing"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Sync reconciles the config into the store, matching records by name:
// new names are added, changed ones updated, and names no longer in the file
// are marked missing rather than deleted.
func (s *Store) Sync(ctx context.Context, cfg *config.Config, configPath string) (Summary, error) {
	sum := Summary{Added: []string{}, Updated: []string{}, Missing: []string{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO environment (id, name, config_path, synced_at) VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, config_path = excluded.config_path, synced_at = excluded.synced_at`,
		cfg.Environment.Name, configPath, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return sum, err
	}

	oldDeps, err := dependencies(ctx, tx)
	if err != nil {
		return sum, err
	}
	existingDeps := map[string]Dependency{}
	for _, d := range oldDeps {
		existingDeps[d.Name] = d
	}
	depIDs := map[string]string{}
	for _, d := range cfg.Dependencies {
		next := Dependency{Name: d.Name, Kind: d.Kind, Address: d.Address, Upstream: d.Upstream, Listen: d.Listen, Redirect: d.Redirect}
		old, ok := existingDeps[d.Name]
		switch {
		case !ok:
			next.ID = newID()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO dependency (id, name, kind, address, upstream, listen, redirect) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				next.ID, next.Name, next.Kind, next.Address, next.Upstream, next.Listen, next.Redirect); err != nil {
				return sum, err
			}
			sum.Added = append(sum.Added, "dependency "+d.Name)
		default:
			next.ID = old.ID
			if old != next {
				if _, err := tx.ExecContext(ctx,
					`UPDATE dependency SET kind = ?, address = ?, upstream = ?, listen = ?, redirect = ?, missing = 0 WHERE id = ?`,
					next.Kind, next.Address, next.Upstream, next.Listen, next.Redirect, next.ID); err != nil {
					return sum, err
				}
				sum.Updated = append(sum.Updated, "dependency "+d.Name)
			}
		}
		depIDs[d.Name] = next.ID
		delete(existingDeps, d.Name)
	}
	for name, old := range existingDeps {
		if old.Missing {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE dependency SET missing = 1 WHERE id = ?`, old.ID); err != nil {
			return sum, err
		}
		sum.Missing = append(sum.Missing, "dependency "+name)
	}

	oldServices, err := services(ctx, tx)
	if err != nil {
		return sum, err
	}
	existingServices := map[string]Service{}
	for _, svc := range oldServices {
		existingServices[svc.Name] = svc
	}
	for _, svc := range cfg.Services {
		uses := slices.Sorted(slices.Values(svc.Uses))
		commands := make([]Command, len(svc.Commands))
		for i, c := range svc.Commands {
			commands[i] = Command{Name: c.Name, Run: c.Run, Cwd: c.Cwd, TimeoutSec: c.TimeoutSec}
		}
		old, ok := existingServices[svc.Name]
		id := old.ID
		switch {
		case !ok:
			id = newID()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO service (id, name, base_url, health_check, health_command, health_every_sec) VALUES (?, ?, ?, ?, ?, ?)`,
				id, svc.Name, svc.BaseURL, svc.HealthCheck, svc.HealthCommand, svc.HealthEverySec); err != nil {
				return sum, err
			}
			sum.Added = append(sum.Added, "service "+svc.Name)
		case old.Missing || old.BaseURL != svc.BaseURL || old.HealthCheck != svc.HealthCheck ||
			old.HealthCommand != svc.HealthCommand || old.HealthEverySec != svc.HealthEverySec ||
			!slices.Equal(old.Uses, uses) || !slices.Equal(old.Commands, commands):
			if _, err := tx.ExecContext(ctx,
				`UPDATE service SET base_url = ?, health_check = ?, health_command = ?, health_every_sec = ?, missing = 0 WHERE id = ?`,
				svc.BaseURL, svc.HealthCheck, svc.HealthCommand, svc.HealthEverySec, id); err != nil {
				return sum, err
			}
			sum.Updated = append(sum.Updated, "service "+svc.Name)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM service_dependency WHERE service_id = ?`, id); err != nil {
			return sum, err
		}
		for _, dep := range uses {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO service_dependency (service_id, dependency_id) VALUES (?, ?)`, id, depIDs[dep]); err != nil {
				return sum, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM command WHERE service_id = ?`, id); err != nil {
			return sum, err
		}
		for _, c := range commands {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO command (service_id, name, run, cwd, timeout_sec) VALUES (?, ?, ?, ?, ?)`,
				id, c.Name, c.Run, c.Cwd, c.TimeoutSec); err != nil {
				return sum, err
			}
		}
		delete(existingServices, svc.Name)
	}
	for name, old := range existingServices {
		if old.Missing {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE service SET missing = 1 WHERE id = ?`, old.ID); err != nil {
			return sum, err
		}
		sum.Missing = append(sum.Missing, "service "+name)
	}

	return sum, tx.Commit()
}

func (s *Store) Environment(ctx context.Context) (Environment, error) {
	var env Environment
	var syncedAt string
	err := s.db.QueryRowContext(ctx, `SELECT name, config_path, synced_at FROM environment WHERE id = 1`).
		Scan(&env.Name, &env.ConfigPath, &syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return env, nil
	}
	if err != nil {
		return env, err
	}
	env.SyncedAt, _ = time.Parse(time.RFC3339, syncedAt)
	return env, nil
}

func (s *Store) Dependencies(ctx context.Context) ([]Dependency, error) {
	return dependencies(ctx, s.db)
}

func (s *Store) Services(ctx context.Context) ([]Service, error) { return services(ctx, s.db) }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func dependencies(ctx context.Context, q querier) ([]Dependency, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name, kind, address, upstream, listen, redirect, missing FROM dependency ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dependency{}
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &d.Address, &d.Upstream, &d.Listen, &d.Redirect, &d.Missing); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func services(ctx context.Context, q querier) ([]Service, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name, base_url, health_check, health_command, health_every_sec, missing FROM service ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []Service{}
	byID := map[string]int{}
	for rows.Next() {
		svc := Service{Uses: []string{}, Commands: []Command{}}
		if err := rows.Scan(&svc.ID, &svc.Name, &svc.BaseURL, &svc.HealthCheck, &svc.HealthCommand, &svc.HealthEverySec, &svc.Missing); err != nil {
			rows.Close()
			return nil, err
		}
		byID[svc.ID] = len(out)
		out = append(out, svc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.QueryContext(ctx,
		`SELECT sd.service_id, d.name FROM service_dependency sd JOIN dependency d ON d.id = sd.dependency_id ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var serviceID, depName string
		if err := rows.Scan(&serviceID, &depName); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := byID[serviceID]; ok {
			out[i].Uses = append(out[i].Uses, depName)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// rowid keeps the order commands have in the config file.
	rows, err = q.QueryContext(ctx, `SELECT service_id, name, run, cwd, timeout_sec FROM command ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var serviceID string
		var c Command
		if err := rows.Scan(&serviceID, &c.Name, &c.Run, &c.Cwd, &c.TimeoutSec); err != nil {
			return nil, err
		}
		if i, ok := byID[serviceID]; ok {
			out[i].Commands = append(out[i].Commands, c)
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveCommandRun(ctx context.Context, r *CommandRun) error {
	if r.ID == "" {
		r.ID = newID()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO command_run (id, service, command, run, started_at, duration_ms, exit_code, timed_out, error, output)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Service, r.Command, r.Run, r.StartedAt.UTC().Format(time.RFC3339Nano),
		r.DurationMs, r.ExitCode, r.TimedOut, r.Error, r.Output)
	return err
}

// CommandRun returns one run, with its output.
func (s *Store) CommandRun(ctx context.Context, id string) (CommandRun, error) {
	rows, err := s.db.QueryContext(ctx, commandRunSelect+` WHERE id = ?`, id)
	if err != nil {
		return CommandRun{}, err
	}
	runs, err := scanCommandRuns(rows)
	if err != nil {
		return CommandRun{}, err
	}
	if len(runs) == 0 {
		return CommandRun{}, sql.ErrNoRows
	}
	return runs[0], nil
}

// RecentCommandRuns returns the latest runs of every command, newest first,
// keyed by "service/command". Only the newest run of each carries its output.
func (s *Store) RecentCommandRuns(ctx context.Context, perCommand int) (map[string][]CommandRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, service, command, run, started_at, duration_ms, exit_code, timed_out, error,
		       CASE WHEN rn = 1 THEN output ELSE '' END
		FROM (SELECT *, ROW_NUMBER() OVER (PARTITION BY service, command ORDER BY started_at DESC) AS rn FROM command_run)
		WHERE rn <= ?
		ORDER BY started_at DESC`, perCommand)
	if err != nil {
		return nil, err
	}
	runs, err := scanCommandRuns(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]CommandRun{}
	for _, r := range runs {
		key := r.Service + "/" + r.Command
		out[key] = append(out[key], r)
	}
	return out, nil
}

const commandRunSelect = `SELECT id, service, command, run, started_at, duration_ms, exit_code, timed_out, error, output FROM command_run`

func scanCommandRuns(rows *sql.Rows) ([]CommandRun, error) {
	defer rows.Close()
	var out []CommandRun
	for rows.Next() {
		var r CommandRun
		var startedAt string
		if err := rows.Scan(&r.ID, &r.Service, &r.Command, &r.Run, &startedAt, &r.DurationMs,
			&r.ExitCode, &r.TimedOut, &r.Error, &r.Output); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
