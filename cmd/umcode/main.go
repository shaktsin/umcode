// Command umcode is the UMCode engine and CLI.
//
//	umcode engine            run the engine in the foreground
//	umcode chat "hello"      send a message (streams the reply)
//	umcode thread list       browse chat history
//	umcode key add ...       manage API keys
//	umcode usage             token usage and cost per key
//
// Run `umcode help` for everything.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/server"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/version"
)

const usage = `UMCode — self-hosted personal AI assistant

Usage: umcode [--config FILE] <command> [args]

Engine
  engine                         Run the engine in the foreground
  status                         Show engine status
  service install|uninstall|status
                                 Run the engine at login (launchd on macOS, systemd --user on Linux)

Projects
  project list [--all]           Folders the agent may work in
  project add [PATH] [--name N] [-p P] [-m M] [-c LEVEL] [--shell=false] [--network]
  project show ID                Folder, git state, model defaults and AGENT.md
  project instructions ID [-f FILE|-] [--composed]
                                 Read or write the project's AGENT.md
  project files ID [PATH] [--depth N] | diff ID [--turn TURN] | revert TURN [PATH…]
  project set ID [--name N] [-p P] [-m M] [-c LEVEL] [-k KEY] [--shell on|off] [--network on|off]
  project archive ID | unarchive ID | remove ID

Chat
  chat [flags] [MESSAGE]         Send a message; without MESSAGE, start an interactive session
      --project ID  work in this project (its folder is the sandbox)
      -t THREAD   continue a thread (default: new thread)
      -p PROVIDER claude | openai | gemini | openai_compatible
      -m MODEL    model id
      -c LEVEL    auto | quick | standard | deep
      -k KEY      API key id to use
  thread list [--archived] | show ID | search QUERY | rename ID TITLE | pin ID | unpin ID
         archive ID | unarchive ID | delete ID | fork ID [ITEM] | export ID [-o FILE]
         set ID [-p PROVIDER] [-m MODEL] [-c LEVEL] [-k KEY]
  approvals                      List pending approvals

Plugins and automation
  plugin inspect SOURCE | install [--linked] [--project ID] SOURCE | list [--project ID]
         show ID | enable ID --project ID | disable ID --project ID | reload ID
         remove [--disable-projects] ID
  task list [--all] | add (--at T | --daily HH:MM | --weekly DAY@HH:MM | --hourly M | --cron EXPR)
       [--name N] [--tz ZONE] [-p P] [-m M] [-c LEVEL] PROMPT | cancel ID | run ID | runs ID
  skill list | show NAME | install PATH_OR_GIT_URL [--name N] | remove NAME
  mcp [list] | mcp restart NAME
  tools                          List every tool the agent can use
  approve ID | deny ID           Answer an approval

Models and keys
  key list | add --provider P [--label L] [--base-url URL] [--default] | test ID
      default ID | enable ID | disable ID | fallback ID on|off | rotate ID | delete ID
      budget ID USD [--hard-stop]
  model list [-p PROVIDER] [--all] | hide PROVIDER MODEL | show PROVIDER MODEL | refresh KEY
        price PROVIDER MODEL IN CACHED_IN OUT   (USD per 1M tokens)
        configured                              The approved models and pools
        route [-c LEVEL] [-p PROVIDER] [-m MODEL] [-k KEY] [--pool ID] [TEXT]
                                 What the next message would run on, and its fallbacks
        health [-clear PROVIDER MODEL KEY]      Cooldowns and how each key has been doing
  complexity [show] | default LEVEL
  usage [--by credential|model|thread|role|day] [--days N] [--key ID]

  version                        Print the version
`

var configPath string

func main() {
	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "--config") {
		if args[0] == "--config" && len(args) > 1 {
			configPath, args = args[1], args[2:]
		} else if v, ok := strings.CutPrefix(args[0], "--config="); ok {
			configPath, args = v, args[1:]
		} else {
			break
		}
	}
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "engine", "serve":
		err = runEngine(rest)
	case "status":
		err = runStatus()
	case "service":
		err = runService(rest)
	case "chat":
		err = runChat(rest)
	case "thread", "threads":
		err = runThread(rest)
	case "approvals":
		err = runApprovals()
	case "approve", "deny":
		if len(rest) != 1 {
			err = errors.New("usage: umcode " + cmd + " APPROVAL_ID")
		} else {
			err = runRespond(rest[0], cmd == "approve")
		}
	case "key", "keys":
		err = runKey(rest)
	case "model", "models":
		err = runModel(rest)
	case "complexity":
		err = runComplexity(rest)
	case "project", "projects":
		err = runProject(rest)
	case "task", "tasks":
		err = runTask(rest)
	case "plugin", "plugins":
		err = runPlugin(rest)
	case "skill", "skills":
		err = runSkill(rest)
	case "mcp":
		err = runMCP(rest)
	case "tools":
		err = runTools()
	case "usage":
		err = runUsage(rest)
	case "version", "--version", "-v":
		fmt.Printf("umcode %s (%s), build %s, protocol %s\n", version.Version, version.Commit, version.BuildID, protocol.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command %q (see `umcode help`)", cmd)
	}
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			fmt.Fprintln(os.Stderr, "error:", pe.Message)
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) { return config.Load(configPath) }

// ---- engine ----

func runEngine(args []string) error {
	fs := flag.NewFlagSet("engine", flag.ExitOnError)
	level := fs.String("log-level", "info", "debug | info | warn | error")
	fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*level)); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Runtime.LogDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(cfg.Runtime.LogDir, "engine.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, logFile), &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	sec := secrets.Default(cfg.Home)
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Secrets: sec, Logger: log})
	if err != nil {
		return err
	}
	srv := server.New(eng, log)
	// Bind the TCP port first: ListenUnix replaces a stale socket file, and a
	// second engine that then fails on the port must not leave the running
	// engine's socket deleted.
	if err := srv.ListenWebSocket(cfg.Runtime.WSHost, cfg.Runtime.EngineWSPort, filepath.Join(filepath.Dir(cfg.Runtime.SocketPath), "token")); err != nil {
		return fmt.Errorf("websocket: %w (another UMCode engine may still be running; find it with `lsof -nP -iTCP:%d -sTCP:LISTEN`)", err, cfg.Runtime.EngineWSPort)
	}
	if err := srv.ListenUnix(cfg.Runtime.SocketPath); err != nil {
		srv.Close()
		return err
	}
	log.Info("engine started", "version", version.Version, "config", cfg.Path, "db", cfg.Storage.DBPath, "secrets", sec.Backend())
	<-ctx.Done()
	log.Info("shutting down")
	srv.Close()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eng.Shutdown(sctx)
	_ = os.Remove(cfg.Runtime.SocketPath)
	return nil
}

// ---- client helpers ----

func dial(ctx context.Context) (*client.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return client.Dial(ctx, cfg.Runtime.SocketPath, "umcode-cli", true)
}

// call dials, calls one method and closes.
func call(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(ctx, method, params, out)
}

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0) }

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func runStatus() error {
	var st protocol.EngineStatus
	if err := call(protocol.MethodEngineStatus, nil, &st); err != nil {
		return err
	}
	fmt.Printf("engine %s (protocol %s), up %s\n", st.EngineVersion, st.ProtocolVersion, time.Since(st.StartedAt).Round(time.Second))
	fmt.Printf("clients: %d  active turns: %d  pending approvals: %d\ndatabase: %s\n", st.Clients, st.ActiveTurns, st.PendingApprovals, st.DBPath)
	return nil
}

func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func fmtUsage(u protocol.UsageTotals) string {
	s := fmt.Sprintf("%s in", fmtTokens(u.InputTokens))
	if u.CachedInputTokens > 0 {
		s += fmt.Sprintf(" (%s cached)", fmtTokens(u.CachedInputTokens))
	}
	s += fmt.Sprintf(" · %s out", fmtTokens(u.OutputTokens))
	if u.ReasoningTokens > 0 {
		s += fmt.Sprintf(" (%s reasoning)", fmtTokens(u.ReasoningTokens))
	}
	s += fmt.Sprintf(" · $%.4f", u.CostUSD)
	if u.Estimated {
		s += " (estimated)"
	}
	return s
}
