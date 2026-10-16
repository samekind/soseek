package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/samekind/soseek/internal/client"
	"github.com/samekind/soseek/internal/core"
	"golang.org/x/term"
)

var version = core.Version

type app struct {
	in           io.Reader
	out, err     io.Writer
	json, stream bool
	secret       string
	mu           sync.Mutex
}
type commandError struct {
	code      int
	message   string
	runID     string
	pendingID string
}

func (e *commandError) Error() string { return e.message }
func inputError(s string) error       { return &commandError{code: 2, message: s} }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &app{in: os.Stdin, out: os.Stdout, err: os.Stderr}
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	for _, v := range args {
		if v == "--json" || v == "--json=true" {
			a.json = true
		}
		if v == "--stream" || v == "--stream=true" {
			a.stream = true
		}
	}
	code, err := a.execute(ctx, args)
	if err == nil {
		return code
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	detail := map[string]any{"code": "client_error", "message": a.clean(err.Error())}
	var ce *commandError
	if errors.As(err, &ce) {
		code = ce.code
		if ce.runID != "" {
			detail["run_id"] = ce.runID
		}
		if ce.pendingID != "" {
			detail["pending_id"] = ce.pendingID
		}
	}
	var ae *client.APIError
	if errors.As(err, &ae) {
		code = 4
		if ae.Status == 401 || ae.Status == 403 {
			code = 3
		}
		detail["code"] = ae.Detail.Code
		detail["retryable"] = ae.Detail.Retryable
		detail["uncertain"] = ae.Detail.Uncertain
		if ae.Detail.RetryAfterSeconds > 0 {
			detail["retry_after_seconds"] = ae.Detail.RetryAfterSeconds
		}
	}
	if code == 0 {
		code = 4
	}
	if a.json || a.stream {
		_ = a.emit(map[string]any{"error": detail})
	} else {
		fmt.Fprintln(a.err, "soseek:", a.clean(err.Error()))
	}
	return code
}

func (a *app) execute(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.out, helpText)
		return 0, nil
	}
	switch args[0] {
	case "version", "--version":
		if a.json {
			return 0, a.emit(map[string]string{"version": version})
		}
		fmt.Fprintln(a.out, "soseek", version)
		return 0, nil
	case "auth":
		return a.auth(ctx, args[1:])
	case "search", "read", "research", "answer", "call":
		return a.submit(ctx, args[0], args[1:])
	case "runs":
		return a.runs(ctx, args[1:])
	case "pending":
		return a.pending(ctx, args[1:])
	case "capabilities", "providers", "profiles", "operations", "usage", "doctor":
		return a.discovery(ctx, args[0], args[1:])
	default:
		return 2, inputError("unknown command; run soseek --help")
	}
}
func (a *app) connect() (*client.Client, error) {
	c, err := client.Load()
	if err != nil {
		return nil, &commandError{code: 3, message: err.Error()}
	}
	a.secret = c.Token
	return c, nil
}
func (a *app) flagset(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

func (a *app) auth(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, inputError("usage: soseek auth login --server URL [--token-file FILE | --token-stdin] | logout")
	}
	fs := a.flagset("auth " + args[0])
	server := fs.String("server", "", "HTTPS cloud URL (HTTP allowed for loopback)")
	tokenFile := fs.String("token-file", "", "read access token from a private file")
	stdin := fs.Bool("token-stdin", false, "read access token from stdin")
	fs.BoolVar(&a.json, "json", a.json, "output one JSON object")
	if err := parseFlags(fs, args[1:]); err != nil {
		return 2, err
	}
	if fs.NArg() != 0 {
		return 2, inputError("unexpected auth argument")
	}
	dir, err := client.ConfigDir()
	if err != nil {
		return 4, err
	}
	if args[0] == "logout" {
		// Only our own managed token is removed. An externally supplied env
		// credential is never deleted, and remote revocation is separate.
		for _, path := range []string{filepath.Join(dir, "client-token"), filepath.Join(dir, "client.json")} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return 4, err
			}
		}
		return 0, a.print(map[string]any{"status": "logged_out", "remote_revoked": false}, "Local login removed. Server token revocation is a separate admin operation.")
	}
	if args[0] != "login" {
		return 2, inputError("unknown auth command")
	}
	if *server == "" {
		return 2, inputError("--server is required")
	}
	if *stdin && *tokenFile != "" {
		return 2, inputError("--token-file and --token-stdin are mutually exclusive")
	}
	var token []byte
	if *tokenFile != "" {
		token, err = client.ReadPrivate(*tokenFile)
	} else if file, ok := a.in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(a.err, "Access token: ")
		token, err = term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(a.err)
	} else {
		token, err = io.ReadAll(io.LimitReader(a.in, 16385))
	}
	if err != nil {
		return 3, err
	}
	if len(token) > 16384 {
		return 2, inputError("access token is too long")
	}
	c, err := client.New(*server, string(token))
	if err != nil {
		return 2, inputError(err.Error())
	}
	a.secret = c.Token
	var capability any
	if err = c.Do(ctx, "GET", "/v1/capabilities", nil, &capability, nil); err != nil {
		return 3, err
	}
	path := filepath.Join(dir, "client-token")
	if err = client.WriteSecret(path, []byte(c.Token+"\n")); err != nil {
		return 4, err
	}
	if err = client.WritePrivate(filepath.Join(dir, "client.json"), client.Config{Server: c.BaseURL, TokenFile: path}); err != nil {
		return 4, err
	}
	return 0, a.print(map[string]any{"status": "authenticated", "server": c.BaseURL}, "Authenticated to "+c.BaseURL)
}

func (a *app) discovery(ctx context.Context, command string, args []string) (int, error) {
	fs := a.flagset(command)
	fs.BoolVar(&a.json, "json", a.json, "output one JSON object")
	fresh := fs.Bool("fresh", false, "refresh capability metadata from the server")
	if err := parseFlags(fs, args); err != nil {
		return 2, err
	}
	path := "/v1/" + command
	if command == "doctor" {
		path = "/v1/capabilities"
	}
	if command == "operations" {
		if fs.NArg() != 1 {
			return 2, inputError("usage: soseek operations PROVIDER --json")
		}
		path += "/" + url.PathEscape(fs.Arg(0))
	} else if fs.NArg() != 0 {
		return 2, inputError("unexpected positional argument")
	}
	c, err := a.connect()
	if err != nil {
		return 3, err
	}
	var out any
	if command == "doctor" || command == "usage" {
		err = c.Do(ctx, "GET", path, nil, &out, nil)
	} else {
		err = c.Discovery(ctx, path, &out, *fresh)
	}
	if err != nil {
		return 4, err
	}
	if command == "doctor" {
		out = map[string]any{"status": "ok", "server": c.BaseURL, "checks": map[string]string{"authenticated_api": "passed", "upstream_calls": "not_performed"}, "capabilities": out}
	}
	if _, isObject := out.(map[string]any); !isObject {
		out = map[string]any{command: out}
	}
	return 0, a.emit(out)
}

func (a *app) submit(ctx context.Context, operation string, args []string) (int, error) {
	fs := a.flagset(operation)
	options := newOptions(fs, a)
	if err := parseFlags(fs, args); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, inputError("provide exactly one quoted query, URL, or provider.operation")
	}
	req, err := options.request(operation, fs.Arg(0), a.in)
	if err != nil {
		return 2, err
	}
	c, err := a.connect()
	if err != nil {
		return 3, err
	}
	if options.plan {
		plan, err := c.Plan(ctx, req)
		if err != nil {
			return 4, err
		}
		return 0, a.emit(plan)
	}
	if _, err = client.PendingList(); err != nil {
		return 4, err
	} // removes expired unsent bodies
	p, err := client.NewPending(c, req)
	if err != nil {
		return 4, err
	}
	run, err := c.Submit(ctx, req, p.ID)
	if err != nil {
		return 4, &commandError{code: errorCode(err), message: err.Error() + "; submission may be accepted; resume with: soseek pending resume " + p.ID, pendingID: p.ID}
	}
	if err = client.Acknowledge(&p, run.ID); err != nil {
		return 4, &commandError{code: 4, message: "run accepted, but local acknowledgement failed: " + err.Error(), runID: run.ID}
	}
	if options.detach {
		return 0, a.print(run, "Accepted run "+run.ID+" ("+run.Status+")")
	}
	return a.wait(ctx, c, run, options)
}

func (a *app) runs(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, inputError("usage: soseek runs get|wait|cancel RUN_ID")
	}
	fs := a.flagset("runs " + args[0])
	options := newOutputOptions(fs, a)
	if err := parseFlags(fs, args[1:]); err != nil {
		return 2, err
	}
	if err := options.validateOutput(); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, inputError("a run ID is required")
	}
	id := fs.Arg(0)
	if id == "" || strings.ContainsAny(id, "/\\?#") {
		return 2, inputError("invalid run ID")
	}
	c, err := a.connect()
	if err != nil {
		return 3, err
	}
	if args[0] == "cancel" {
		run, err := c.Cancel(ctx, id)
		if err != nil {
			return 4, err
		}
		return 0, a.print(run, "Cancellation requested for "+run.ID+" ("+run.Status+")")
	}
	if args[0] != "get" && args[0] != "wait" {
		return 2, inputError("unknown runs command")
	}
	run, err := c.Run(ctx, id)
	if err != nil {
		return 4, err
	}
	if args[0] == "wait" {
		return a.wait(ctx, c, run, options)
	}
	if core.Terminal(run.Status) {
		return a.finish(ctx, c, run, options)
	}
	return 0, a.print(run, run.ID+": "+run.Status)
}

func (a *app) pending(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, inputError("usage: soseek pending list | resume PENDING_ID")
	}
	fs := a.flagset("pending " + args[0])
	options := newOutputOptions(fs, a)
	fs.BoolVar(&options.detach, "detach", false, "return after acceptance without waiting")
	if err := parseFlags(fs, args[1:]); err != nil {
		return 2, err
	}
	if err := options.validateOutput(); err != nil {
		return 2, err
	}
	if args[0] == "list" {
		if fs.NArg() != 0 {
			return 2, inputError("unexpected pending argument")
		}
		items, err := client.PendingList()
		if err != nil {
			return 4, err
		}
		return 0, a.emit(map[string]any{"pending": items})
	}
	if args[0] != "resume" || fs.NArg() != 1 {
		return 2, inputError("usage: soseek pending resume PENDING_ID")
	}
	c, err := a.connect()
	if err != nil {
		return 3, err
	}
	p, err := client.LoadPending(fs.Arg(0))
	if err != nil {
		return 4, err
	}
	if p.Server != c.BaseURL || p.Identity != client.Identity(c.Token) {
		return 3, &commandError{code: 3, message: "pending request belongs to a different server or login; restore that login before resuming"}
	}
	var run core.Run
	if p.RunID != "" {
		run, err = c.Run(ctx, p.RunID)
	} else if p.Request != nil {
		run, err = c.Submit(ctx, *p.Request, p.ID)
		if err == nil {
			err = client.Acknowledge(&p, run.ID)
		}
	} else {
		return 4, errors.New("pending request has neither original input nor run ID")
	}
	if err != nil {
		return errorCode(err), err
	}
	if options.detach {
		return 0, a.print(run, "Accepted run "+run.ID)
	}
	return a.wait(ctx, c, run, options)
}

func (a *app) wait(ctx context.Context, c *client.Client, run core.Run, o *options) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, o.waitTimeout)
	defer cancel()
	var eventDone chan struct{}
	if a.stream && !core.Terminal(run.Status) {
		eventCtx, eventCancel := context.WithCancel(ctx)
		eventRunID := run.ID
		eventDone = make(chan struct{})
		go func() {
			defer close(eventDone)
			var last int64
			for eventCtx.Err() == nil {
				err := c.Events(eventCtx, eventRunID, last, func(e core.Event) error {
					if e.ID <= last {
						return nil
					}
					last = e.ID
					return a.emit(map[string]any{"type": "event", "event": e})
				})
				if eventCtx.Err() != nil {
					return
				}
				if err != nil {
					a.diagnostic("Event stream unavailable; checking run snapshots.")
					return
				}
				select {
				case <-eventCtx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
		defer func() { eventCancel(); <-eventDone }()
	}
	lastStatus := ""
	for {
		if run.Status != lastStatus {
			if a.stream {
				_ = a.emit(map[string]any{"type": "status", "run_id": run.ID, "status": run.Status})
			} else {
				a.diagnostic(run.ID + ": " + run.Status)
			}
			lastStatus = run.Status
		}
		if core.Terminal(run.Status) {
			cancel()
			if eventDone != nil {
				<-eventDone
			}
			return a.finish(context.WithoutCancel(ctx), c, run, o)
		}
		select {
		case <-ctx.Done():
			message := "local wait timed out; cloud run continues; resume with: soseek runs wait " + run.ID
			if errors.Is(ctx.Err(), context.Canceled) {
				message = "local wait interrupted; cloud run was not cancelled; resume with: soseek runs wait " + run.ID
			}
			return 5, &commandError{code: 5, message: message, runID: run.ID}
		case <-time.After(time.Second):
		}
		var err error
		id := run.ID
		var next core.Run
		next, err = c.Run(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return 5, &commandError{code: 5, message: "local wait ended; cloud run was not cancelled", runID: id}
			}
			return errorCode(err), &commandError{code: errorCode(err), message: err.Error() + "; resume with: soseek runs wait " + id, runID: id}
		}
		run = next
	}
}

func (a *app) finish(ctx context.Context, c *client.Client, run core.Run, o *options) (int, error) {
	result := run.Result
	if result == nil {
		r, err := c.Result(ctx, run.ID)
		if err != nil {
			if run.Error != nil {
				return statusCode(run.Status, o.requireAll), run.Error
			}
			return errorCode(err), err
		}
		result = &r
	}
	if result.RunID == "" {
		result.RunID = run.ID
	}
	if result.Status == "" {
		result.Status = run.Status
	}
	view, err := resultView(*result, o, c.BaseURL)
	if err != nil {
		return 4, err
	}
	if a.json || a.stream {
		if a.stream {
			view = map[string]any{"type": "result", "result": view}
		}
		err = a.emit(view)
	} else {
		err = a.humanResult(view)
	}
	if result.Status == "partial" {
		fmt.Fprintln(a.err, "Warning: partial result; inspect warnings and attempts.")
	}
	return statusCode(result.Status, o.requireAll), err
}
func statusCode(status string, requireAll bool) int {
	switch status {
	case "failed":
		return 4
	case "cancelled":
		return 6
	case "partial":
		if requireAll {
			return 4
		}
	}
	return 0
}
func errorCode(err error) int {
	var ae *client.APIError
	if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403) {
		return 3
	}
	return 4
}

const helpText = `soseek — Search, read, and research through your cloud service.

Usage:
  soseek auth login --server URL [--token-file FILE | --token-stdin]
  soseek auth logout
  soseek search "QUERY" [--provider NAME | --providers A,B] [--json]
  soseek read URL [--provider NAME] [--json]
  soseek research "QUERY" [--mode agent|pipeline] [--profile deep] [--json]
  soseek answer "QUERY" [--json]
  soseek call PROVIDER.OPERATION --input FILE [--json]
  soseek capabilities|providers|profiles|usage|doctor [--json]
  soseek operations PROVIDER [--json]
  soseek runs get|wait|cancel RUN_ID [--json]
  soseek pending list [--json]
  soseek pending resume PENDING_ID [--detach] [--json]

Execution options:
  --plan                 Validate and show a plan; no upstream calls
  --budget-usd AMOUNT     Per-run upper budget in USD
  --deadline 180s         Cloud execution deadline (seconds or Go duration)
  --wait-timeout 5m       Local wait only; does not cancel the cloud run
  --detach               Return the run ID after cloud acceptance
  --opt provider.key=VAL  Provider option; VAL may be JSON or a plain string
  --fresh                Bypass cached upstream results
  --json                 Exactly one final JSON object on stdout
  --stream               NDJSON progress and final result; excludes --json
  --fields LIST          Return selected top-level or results.FIELD values
  --max-chars N          Clip returned text; does not reduce work or charges
  --require-all          Exit 4 for partial results

Ctrl-C stops local waiting. Use runs cancel to stop the cloud task explicitly.
Submission failures preserve their original request and idempotency key for
24 hours; pending resume reuses both. Tokens and pending bodies are private.
SOSEEK_URL and SOSEEK_TOKEN_FILE override the saved client configuration.
Exit codes: 0 success/partial, 2 input, 3 auth, 4 failure, 5 wait ended, 6 cancelled.
`
