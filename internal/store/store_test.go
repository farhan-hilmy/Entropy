package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"entropy/internal/config"
)

func open(t *testing.T) *Store {
	t.Helper()
	// Not t.TempDir: on Windows, a virus scanner can still hold the database
	// file at cleanup, which fails the test. Leftovers here are harmless.
	dir, err := os.MkdirTemp("", "entropy-store-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := Open(filepath.Join(dir, "entropy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func dep(name, address string) config.Dependency {
	return config.Dependency{Name: name, Kind: "redis", Address: address, Upstream: name + ":6379", Listen: "0.0.0.0:6379"}
}

func TestSyncReconcilesByName(t *testing.T) {
	ctx := context.Background()
	st := open(t)

	first := &config.Config{
		Environment:  config.Environment{Name: "local"},
		Dependencies: []config.Dependency{dep("redis", "localhost:6379"), dep("minio", "localhost:9000")},
		Services:     []config.Service{{Name: "oauth", BaseURL: "http://localhost:8000", Uses: []string{"redis"}}},
	}
	sum, err := st.Sync(ctx, first, "entropy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dependency redis", "dependency minio", "service oauth"}; !slices.Equal(sum.Added, want) {
		t.Fatalf("added = %v, want %v", sum.Added, want)
	}
	before, _ := st.Dependencies(ctx)

	// Same file again: nothing changes.
	sum, err = st.Sync(ctx, first, "entropy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Added)+len(sum.Updated)+len(sum.Missing) != 0 {
		t.Fatalf("resync of an unchanged file reported changes: %+v", sum)
	}

	// Redis moves, minio is removed, kafka is added, oauth now also uses kafka.
	second := &config.Config{
		Environment:  config.Environment{Name: "local"},
		Dependencies: []config.Dependency{dep("redis", "localhost:6380"), dep("kafka", "localhost:9092")},
		Services:     []config.Service{{Name: "oauth", BaseURL: "http://localhost:8000", Uses: []string{"redis", "kafka"}}},
	}
	sum, err = st.Sync(ctx, second, "entropy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sum.Added, []string{"dependency kafka"}) ||
		!slices.Equal(sum.Updated, []string{"dependency redis", "service oauth"}) ||
		!slices.Equal(sum.Missing, []string{"dependency minio"}) {
		t.Fatalf("unexpected summary: %+v", sum)
	}

	after, _ := st.Dependencies(ctx)
	byName := map[string]Dependency{}
	for _, d := range after {
		byName[d.Name] = d
	}
	for _, d := range before {
		if d.Name == "redis" && byName["redis"].ID != d.ID {
			t.Error("redis kept its name but got a new id")
		}
	}
	if !byName["minio"].Missing {
		t.Error("minio should be kept and marked missing, not deleted")
	}

	svcs, _ := st.Services(ctx)
	if !slices.Equal(svcs[0].Uses, []string{"kafka", "redis"}) {
		t.Errorf("oauth uses = %v", svcs[0].Uses)
	}
}

// A database created before the health columns existed gets them on Open.
func TestOpenAddsNewColumnsToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE service (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, base_url TEXT NOT NULL,
		health_check TEXT NOT NULL DEFAULT '', missing INTEGER NOT NULL DEFAULT 0);
		INSERT INTO service (id, name, base_url) VALUES ('1', 'oauth', 'http://localhost:8080')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svcs, err := st.Services(context.Background())
	if err != nil || len(svcs) != 1 || svcs[0].HealthCommand != "" {
		t.Fatalf("services = %+v, %v", svcs, err)
	}
}

func TestSyncCommandsAndRunHistory(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	cfg := &config.Config{
		Environment: config.Environment{Name: "local"},
		Services: []config.Service{{
			Name: "oauth", BaseURL: "http://localhost:8080",
			Commands: []config.Command{
				{Name: "token", Run: `bru run "Access Token B2B.yml" --env local`, Cwd: "D:/bruno", TimeoutSec: 30},
				{Name: "health", Run: "curl -f http://localhost:8080/", Cwd: "D:/", TimeoutSec: 5},
			},
		}},
	}
	if _, err := st.Sync(ctx, cfg, "entropy.yaml"); err != nil {
		t.Fatal(err)
	}
	svcs, _ := st.Services(ctx)
	if got := svcs[0].Commands; len(got) != 2 || got[0].Name != "token" || got[1].Name != "health" {
		t.Fatalf("commands = %+v, want token then health", got)
	}

	cfg.Services[0].Commands[0].TimeoutSec = 60
	sum, err := st.Sync(ctx, cfg, "entropy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sum.Updated, []string{"service oauth"}) {
		t.Errorf("changing a command should update the service, got %+v", sum)
	}

	base := time.Now()
	for i, exit := range []int{1, 0} {
		run := &CommandRun{Service: "oauth", Command: "token", Run: "bru run", StartedAt: base.Add(time.Duration(i) * time.Second),
			ExitCode: exit, Output: fmt.Sprintf("output %d", i)}
		if err := st.SaveCommandRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := st.RecentCommandRuns(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	runs := recent["oauth/token"]
	if len(runs) != 2 || runs[0].ExitCode != 0 || runs[0].Output != "output 1" || runs[1].Output != "" {
		t.Fatalf("recent runs = %+v; want newest first, output only on the newest", runs)
	}
	full, err := st.CommandRun(ctx, runs[1].ID)
	if err != nil || full.Output != "output 0" {
		t.Fatalf("CommandRun = %+v, %v", full, err)
	}
}
