package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/samekind/soseek/internal/api"
	"github.com/samekind/soseek/internal/config"
	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/engine"
	"github.com/samekind/soseek/internal/provider"
	"github.com/samekind/soseek/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "soseekd:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println("soseekd serve|check|backup|admin token-create|admin token-list|admin token-revoke|admin settle --config FILE\n  token-create: --name NAME --daily-usd 5 [--providers exa,tavily]\n  token-revoke: --id PRINCIPAL_ID\n  settle: --attempt ID (--cost-usd AMOUNT | --worst-case)\n  backup: --out PATH\n  All management commands are local; protect token-create stdout.")
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println(core.Version)
		return nil
	}
	command := args[0]
	rest := args[1:]
	if command == "admin" {
		if len(rest) == 0 {
			return fmt.Errorf("admin subcommand required")
		}
		command = rest[0]
		rest = rest[1:]
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	cfgPath := f.String("config", os.Getenv("SOSEEK_CONFIG"), "server JSON configuration")
	name := f.String("name", "", "principal name")
	daily := f.String("daily-usd", "5", "daily USD limit")
	providers := f.String("providers", "", "allowed providers")
	id := f.String("id", "", "principal ID")
	out := f.String("out", "", "backup output path")
	attemptID := f.String("attempt", "", "attempt ID to settle")
	cost := f.String("cost-usd", "", "confirmed upstream charge")
	worst := f.Bool("worst-case", false, "settle at the original reserved ceiling, as an estimate")
	if err := f.Parse(rest); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *cfgPath == "" {
		return fmt.Errorf("--config is required")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if command == "check" {
		fmt.Printf("configuration valid; revision=%s channels=%d\n", cfg.Revision, len(cfg.Channels))
		return nil
	}
	db, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	switch command {
	case "token-create":
		if *name == "" {
			return fmt.Errorf("--name required")
		}
		m, e := core.ParseUSD(*daily)
		if e != nil || m <= 0 {
			return fmt.Errorf("invalid daily budget")
		}
		var allowed []string
		if *providers != "" {
			allowed = strings.Split(*providers, ",")
		}
		p, token, e := db.CreatePrincipal(ctx, *name, m, allowed)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"principal": p, "token": token})
	case "token-list":
		p, e := db.ListPrincipals(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	case "token-revoke":
		if *id == "" {
			return fmt.Errorf("--id required")
		}
		return db.Revoke(ctx, *id)
	case "backup":
		if *out == "" {
			return fmt.Errorf("--out required")
		}
		return db.Backup(ctx, *out)
	case "settle":
		if *attemptID == "" || (*cost == "" && !*worst) || (*cost != "" && *worst) {
			return fmt.Errorf("settle needs --attempt and exactly one of --cost-usd or --worst-case")
		}
		u := core.Usage{}
		if *worst {
			a, err := db.GetAttempt(ctx, *attemptID)
			if err != nil {
				return err
			}
			u.EstimatedMicros = a.MaxMicros
		} else {
			m, err := core.ParseUSD(*cost)
			if err != nil {
				return err
			}
			u.ReportedMicros = m
			u.CostKnown = true
		}
		if err := db.SettleAttempt(ctx, *attemptID, u); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"attempt_id": *attemptID, "settled": true, "usage": u})
	case "serve":
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err = db.Recover(ctx); err != nil {
			return err
		}
		e := engine.New(cfg, db, provider.New())
		s := api.New(cfg, db, e)
		s.Start(ctx)
		srv := &http.Server{Addr: cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(c)
		}()
		log.Printf("soseekd %s listening on %s revision=%s", core.Version, cfg.Listen, cfg.Revision)
		err = srv.ListenAndServe()
		stop()
		s.Wait()
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
