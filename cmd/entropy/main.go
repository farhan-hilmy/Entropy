// Command entropy runs the Entropy server: it syncs entropy.yaml, keeps
// dependency statuses current and serves the UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"entropy/internal/app"
	"entropy/internal/command"
	"entropy/internal/config"
	"entropy/internal/server"
	"entropy/internal/store"
	"entropy/web"
)

const usage = `Usage:
  entropy up        [-config entropy.yaml] [-addr 127.0.0.1:7400] [-db .entropy/entropy.db]
  entropy validate  [-config entropy.yaml]
  entropy run       [-config entropy.yaml] <service>/<command>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = up(os.Args[2:])
	case "validate":
		err = validate(os.Args[2:])
	case "run":
		err = runCommand(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "entropy:", err)
		os.Exit(1)
	}
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("config", "entropy.yaml", "path to the config file")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("%s is valid: %d dependencies, %d services\n", *configPath, len(cfg.Dependencies), len(cfg.Services))
	return nil
}

// runCommand runs one command from the config file in the terminal, without
// the server, and exits with the command's exit code. Handy for trying a line.
func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "entropy.yaml", "path to the config file")
	fs.Parse(args)
	service, name, ok := strings.Cut(fs.Arg(0), "/")
	if fs.NArg() != 1 || !ok {
		return errors.New("usage: entropy run <service>/<command>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	for _, s := range cfg.Services {
		if s.Name != service {
			continue
		}
		for _, c := range s.Commands {
			if c.Name != name {
				continue
			}
			fmt.Printf("$ %s\n  (in %s, timeout %ds)\n\n", c.Run, c.Cwd, c.TimeoutSec)
			res := command.Run(context.Background(), c.Run, c.Cwd, time.Duration(c.TimeoutSec)*time.Second, app.CommandEnv(s.Name, s.BaseURL))
			fmt.Print(res.Output)
			switch {
			case res.Err != nil:
				return fmt.Errorf("could not run the command: %w", res.Err)
			case res.TimedOut:
				return fmt.Errorf("timed out after %ds", c.TimeoutSec)
			}
			fmt.Printf("\nexit code %d in %s\n", res.ExitCode, res.Duration.Round(time.Millisecond))
			os.Exit(res.ExitCode)
		}
	}
	return fmt.Errorf("no command %s/%s in %s", service, name, *configPath)
}

func up(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "entropy.yaml", "path to the config file")
	addr := fs.String("addr", "127.0.0.1:7400", "address for the UI and API (local only by default)")
	dbPath := fs.String("db", ".entropy/entropy.db", "path to the SQLite database")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Claim the port first, so a busy port fails before anything else starts.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (try -addr 127.0.0.1:<other port>): %w", *addr, err)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	a := app.New(*configPath, st, log)
	res := a.Sync(ctx)
	if res.Error != "" {
		return fmt.Errorf("sync failed: %s", res.Error)
	}
	log.Info("synced", "config", *configPath, "added", res.Summary.Added, "updated", res.Summary.Updated, "missing", res.Summary.Missing)
	for _, w := range res.Warnings {
		log.Warn(w)
	}

	// Toxiproxy faults don't expire on their own, so never start with leftovers.
	if err := a.StopAllFaults(ctx); err != nil {
		log.Warn("could not clear leftover faults", "err", err)
	} else {
		log.Info("cleared leftover faults")
	}

	go a.Run(ctx)

	srv := &http.Server{Handler: server.New(a, web.Dist())}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Info("Entropy is running", "url", "http://"+*addr)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.StopAllFaults(cleanupCtx); err != nil {
		log.Warn("could not clear faults on exit", "err", err)
	} else {
		log.Info("cleared faults on exit")
	}
	return nil
}
