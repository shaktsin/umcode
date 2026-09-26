package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/protocol"
)

const (
	dim   = "\033[2m"
	bold  = "\033[1m"
	red   = "\033[31m"
	reset = "\033[0m"
)

func runChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	threadID := fs.String("t", "", "thread id to continue")
	project := fs.String("project", "", "project id to work in (see `umcode project list`)")
	provider := fs.String("p", "", "provider")
	model := fs.String("m", "", "model")
	cplx := fs.String("c", "", "complexity: auto|quick|standard|deep")
	key := fs.String("k", "", "API key id")
	fs.Parse(args)
	override := protocol.ModelSelection{Provider: *provider, Model: *model, Complexity: protocol.Complexity(*cplx), CredentialID: *key}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	tid := *threadID
	if tid == "" {
		var th protocol.Thread
		if err := c.Call(ctx, protocol.MethodThreadStart, protocol.ThreadStartParams{Channel: "cli",
			ProjectID: *project, Settings: override}, &th); err != nil {
			return err
		}
		tid = th.ID
		fmt.Fprintf(os.Stderr, "%sthread %s%s\n", dim, tid, reset)
	}
	in := bufio.NewReader(os.Stdin)
	message := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if message != "" {
		return chatTurn(ctx, c, in, tid, message, override)
	}
	fmt.Fprintf(os.Stderr, "%sInteractive chat. Empty line or Ctrl-D to quit.%s\n", dim, reset)
	for {
		fmt.Print(bold + "› " + reset)
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" || (err != nil && line == "") {
			fmt.Println()
			return nil
		}
		if err := chatTurn(ctx, c, in, tid, line, override); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(os.Stderr, red+"error: "+err.Error()+reset)
		}
	}
}

// chatTurn starts a turn and renders its events until it completes.
func chatTurn(ctx context.Context, c *client.Client, in *bufio.Reader, threadID, text string, override protocol.ModelSelection) error {
	var res protocol.TurnStartResult
	if err := c.Call(ctx, protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: threadID, Text: text, Override: override}, &res); err != nil {
		return err
	}
	turnID := res.Turn.ID
	r := res.Turn.Resolved
	auto := ""
	if res.Turn.AutoPicked {
		auto = "auto → "
	}
	fmt.Fprintf(os.Stderr, "%s%s · %s · %s%s%s\n", dim, r.Provider, r.Model, auto, r.Complexity, reset)

	kinds := map[string]string{}
	thinkingShown := false
	lastWasText := false
	for {
		select {
		case <-ctx.Done():
			ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.Call(ictx, protocol.MethodTurnInterrupt, protocol.TurnInterruptParams{TurnID: turnID}, nil)
			cancel()
			return ctx.Err()
		case n, ok := <-c.Notifications():
			if !ok {
				return errors.New("engine connection closed")
			}
			switch n.Method {
			case protocol.NotifyItemStarted, protocol.NotifyItemCompleted:
				var ev protocol.ItemEvent
				if json.Unmarshal(n.Params, &ev) != nil || ev.Item.TurnID != turnID {
					continue
				}
				it := ev.Item
				kinds[it.ID] = it.Kind
				switch it.Kind {
				case protocol.ItemToolCall:
					if lastWasText {
						fmt.Println()
						lastWasText = false
					}
					if n.Method == protocol.NotifyItemStarted {
						fmt.Printf("%s→ %s %s [%s]%s\n", dim, it.Tool.Name, compactJSON(it.Tool.Args), it.Tool.Risk, reset)
					} else {
						renderToolResult(it)
					}
				case protocol.ItemFileChange:
					if lastWasText {
						fmt.Println()
						lastWasText = false
					}
					if n.Method == protocol.NotifyItemCompleted {
						fmt.Printf("%s✎ %s%s\n", dim, it.Text, reset)
					}
				case protocol.ItemError:
					fmt.Printf("%s%s%s\n", red, it.Text, reset)
				case protocol.ItemAgentMessage:
					if n.Method == protocol.NotifyItemCompleted && lastWasText {
						fmt.Println()
						lastWasText = false
					}
				}
			case protocol.NotifyItemDelta:
				var d protocol.ItemDelta
				if json.Unmarshal(n.Params, &d) != nil || d.TurnID != turnID {
					continue
				}
				switch kinds[d.ItemID] {
				case protocol.ItemReasoning:
					if !thinkingShown {
						fmt.Printf("%sthinking…%s\n", dim, reset)
						thinkingShown = true
					}
				default:
					fmt.Print(d.Text)
					lastWasText = true
				}
			case protocol.NotifyApprovalRequest:
				var ev protocol.ApprovalEvent
				if json.Unmarshal(n.Params, &ev) != nil || ev.Approval.TurnID != turnID {
					continue
				}
				approve := promptApproval(in, ev.Approval)
				var out protocol.Approval
				if err := c.Call(ctx, protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: ev.Approval.ID, Approve: approve}, &out); err != nil {
					fmt.Fprintf(os.Stderr, "%s(%v)%s\n", dim, err, reset)
				}
			case protocol.NotifyRouteChanged:
				var ev protocol.RouteChangedEvent
				if json.Unmarshal(n.Params, &ev) != nil || ev.TurnID != turnID {
					continue
				}
				if lastWasText {
					fmt.Println()
					lastWasText = false
				}
				fmt.Fprintf(os.Stderr, "%s↷ %s (%s) → %s (%s) after %s%s\n", dim,
					ev.From.Model, ev.From.CredentialL, ev.To.Model, ev.To.CredentialL, ev.Reason, reset)
			case protocol.NotifyBudgetWarning:
				var w protocol.BudgetWarning
				if json.Unmarshal(n.Params, &w) == nil {
					fmt.Fprintf(os.Stderr, "%sbudget: %s has used %.0f%% ($%.2f of $%.2f) this month%s\n", red, w.Label, w.Percent, w.SpentUSD, w.BudgetUSD, reset)
				}
			case protocol.NotifyTurnCompleted:
				var ev protocol.TurnEvent
				if json.Unmarshal(n.Params, &ev) != nil || ev.Turn.ID != turnID {
					continue
				}
				if lastWasText {
					fmt.Println()
				}
				t := ev.Turn
				answered := ""
				if t.Resolved.Model != r.Model || t.Resolved.CredentialID != r.CredentialID {
					answered = t.Resolved.Model + " · "
				}
				fmt.Fprintf(os.Stderr, "%s%s · %s%s%s\n", dim, t.Status, answered, fmtUsage(t.Usage), reset)
				if t.Status == protocol.TurnFailed {
					return errors.New(t.Error)
				}
				return nil
			}
		}
	}
}

func renderToolResult(it protocol.Item) {
	switch it.Status {
	case protocol.ItemDenied:
		fmt.Printf("%s  ✗ %s%s\n", red, it.Tool.Error, reset)
	case protocol.ItemFailed:
		fmt.Printf("%s  ✗ %s%s\n", red, it.Tool.Error, reset)
	default:
		out := strings.TrimSpace(it.Tool.Output)
		lines := strings.Split(out, "\n")
		if len(lines) > 8 {
			lines = append(lines[:8], fmt.Sprintf("… (%d more lines)", len(lines)-8))
		}
		for _, l := range lines {
			fmt.Printf("%s  │ %s%s\n", dim, l, reset)
		}
	}
}

func promptApproval(in *bufio.Reader, a protocol.Approval) bool {
	fmt.Printf("\n%s⚠ Approval needed (%s):%s %s\n", bold, a.Risk, reset, a.ActionSummary)
	if a.Reason != "" {
		fmt.Printf("%s  %s%s\n", dim, a.Reason, reset)
	}
	fmt.Print("  Allow? [y/N] ")
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func compactJSON(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
