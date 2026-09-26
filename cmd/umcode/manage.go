package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// ---- threads ----

func runThread(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	need := func(n int, u string) error {
		if len(rest) < n {
			return errors.New("usage: umcode thread " + u)
		}
		return nil
	}
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("thread list", flag.ExitOnError)
		archived := fs.Bool("archived", false, "show archived threads")
		limit := fs.Int("n", 50, "max threads")
		fs.Parse(rest)
		var r protocol.ThreadListResult
		if err := call(protocol.MethodThreadList, protocol.ThreadListParams{Archived: archived, Limit: *limit}, &r); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "ID\tTITLE\tMODEL SETTING\tUPDATED\tTOKENS\tCOST")
		for _, t := range r.Threads {
			title := t.Title
			if title == "" {
				title = "(untitled)"
			}
			if t.Pinned {
				title = "★ " + title
			}
			model := t.Settings.Model
			if model == "" {
				model = "default"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t$%.4f\n", t.ID, clip(title, 48), model,
				t.UpdatedAt.Local().Format("Jan 02 15:04"), fmtTokens(t.Usage.InputTokens+t.Usage.OutputTokens), t.Usage.CostUSD)
		}
		return w.Flush()
	case "show":
		if err := need(1, "show ID"); err != nil {
			return err
		}
		var r protocol.ThreadReadResult
		if err := call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: rest[0]}, &r); err != nil {
			return err
		}
		fmt.Printf("%s%s%s  %s(%s)%s\n", bold, r.Thread.Title, reset, dim, r.Thread.ID, reset)
		if r.Thread.ProjectID != "" {
			var p protocol.Project
			if call(protocol.MethodProjectOpen, protocol.ProjectIDParams{ProjectID: r.Thread.ProjectID}, &p) == nil {
				fmt.Printf("%sproject %s (%s)%s\n", dim, p.Name, home(p.Root), reset)
			}
		}
		fmt.Println()
		for _, it := range r.Items {
			switch it.Kind {
			case protocol.ItemUserMessage:
				fmt.Printf("%sYou:%s %s\n\n", bold, reset, it.Text)
			case protocol.ItemAgentMessage:
				fmt.Printf("%sUMCode:%s %s\n\n", bold, reset, it.Text)
			case protocol.ItemToolCall:
				fmt.Printf("%s→ %s %s (%s)%s\n", dim, it.Tool.Name, compactJSON(it.Tool.Args), it.Status, reset)
			case protocol.ItemFileChange:
				fmt.Printf("%s✎ %s%s\n", dim, it.Text, reset)
			case protocol.ItemError:
				fmt.Printf("%s%s%s\n", red, it.Text, reset)
			}
		}
		fmt.Printf("%s%s%s\n", dim, fmtUsage(r.Thread.Usage), reset)
		return nil
	case "search":
		if err := need(1, "search QUERY"); err != nil {
			return err
		}
		var r protocol.ThreadSearchResult
		if err := call(protocol.MethodThreadSearch, protocol.ThreadSearchParams{Query: strings.Join(rest, " ")}, &r); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "THREAD\tTITLE\tMATCH")
		for _, h := range r.Hits {
			fmt.Fprintf(w, "%s\t%s\t%s\n", h.ThreadID, clip(h.Title, 40), strings.ReplaceAll(h.Snippet, "\n", " "))
		}
		return w.Flush()
	case "rename":
		if err := need(2, "rename ID TITLE"); err != nil {
			return err
		}
		return call(protocol.MethodThreadRename, protocol.ThreadRenameParams{ThreadID: rest[0], Title: strings.Join(rest[1:], " ")}, nil)
	case "pin", "unpin":
		if err := need(1, sub+" ID"); err != nil {
			return err
		}
		return call(protocol.MethodThreadPin, protocol.ThreadFlagParams{ThreadID: rest[0], Value: sub == "pin"}, nil)
	case "archive", "unarchive":
		if err := need(1, sub+" ID"); err != nil {
			return err
		}
		return call(protocol.MethodThreadArchive, protocol.ThreadFlagParams{ThreadID: rest[0], Value: sub == "archive"}, nil)
	case "delete", "rm":
		if err := need(1, "delete ID"); err != nil {
			return err
		}
		return call(protocol.MethodThreadDelete, protocol.ThreadIDParams{ThreadID: rest[0]}, nil)
	case "fork":
		if err := need(1, "fork ID [ITEM_ID]"); err != nil {
			return err
		}
		p := protocol.ThreadForkParams{ThreadID: rest[0]}
		if len(rest) > 1 {
			p.UpToItemID = rest[1]
		}
		var t protocol.Thread
		if err := call(protocol.MethodThreadFork, p, &t); err != nil {
			return err
		}
		fmt.Println(t.ID)
		return nil
	case "export":
		fs := flag.NewFlagSet("thread export", flag.ExitOnError)
		out := fs.String("o", "", "write to file")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			return errors.New("usage: umcode thread export ID [-o FILE]")
		}
		var r protocol.ThreadExportResult
		if err := call(protocol.MethodThreadExport, protocol.ThreadIDParams{ThreadID: fs.Arg(0)}, &r); err != nil {
			return err
		}
		if *out == "" {
			fmt.Print(r.Markdown)
			return nil
		}
		return os.WriteFile(*out, []byte(r.Markdown), 0o644)
	case "set":
		fs := flag.NewFlagSet("thread set", flag.ExitOnError)
		p := fs.String("p", "", "provider")
		m := fs.String("m", "", "model")
		c := fs.String("c", "", "complexity")
		k := fs.String("k", "", "API key id")
		if len(rest) < 1 {
			return errors.New("usage: umcode thread set ID [-p P] [-m M] [-c LEVEL] [-k KEY]")
		}
		fs.Parse(rest[1:])
		var t protocol.Thread
		if err := call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: rest[0],
			Settings: protocol.ModelSelection{Provider: *p, Model: *m, Complexity: protocol.Complexity(*c), CredentialID: *k}}, &t); err != nil {
			return err
		}
		fmt.Printf("thread %s: provider=%s model=%s complexity=%s key=%s\n", t.ID, or(t.Settings.Provider, "default"),
			or(t.Settings.Model, "default"), or(string(t.Settings.Complexity), "default"), or(t.Settings.CredentialID, "default"))
		return nil
	}
	return fmt.Errorf("unknown thread command %q", sub)
}

// ---- approvals ----

func runApprovals() error {
	var r protocol.ApprovalListResult
	if err := call(protocol.MethodApprovalList, nil, &r); err != nil {
		return err
	}
	if len(r.Approvals) == 0 {
		fmt.Println("No pending approvals.")
		return nil
	}
	w := table()
	fmt.Fprintln(w, "ID\tRISK\tACTION\tEXPIRES")
	for _, a := range r.Approvals {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Risk, clip(a.ActionSummary, 70), time.Until(a.ExpiresAt).Round(time.Second))
	}
	return w.Flush()
}

func runRespond(id string, approve bool) error {
	var a protocol.Approval
	if err := call(protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: id, Approve: approve}, &a); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", a.ID, a.Status)
	return nil
}

// ---- keys ----

func runKey(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	id := func() (string, error) {
		if len(rest) < 1 {
			return "", fmt.Errorf("usage: umcode key %s ID", sub)
		}
		return rest[0], nil
	}
	update := func(p protocol.CredentialUpdateParams) error {
		var c protocol.Credential
		if err := call(protocol.MethodCredentialUpdate, p, &c); err != nil {
			return err
		}
		fmt.Printf("%s (%s): enabled=%v default=%v fallback=%v\n", c.Label, c.ID, c.Enabled, c.IsDefault, c.Fallback)
		return nil
	}
	yes := true
	no := false
	switch sub {
	case "list", "ls":
		var r protocol.CredentialListResult
		if err := call(protocol.MethodCredentialList, nil, &r); err != nil {
			return err
		}
		if len(r.Credentials) == 0 {
			fmt.Println("No API keys yet. Add one with: umcode key add --provider claude")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "ID\tPROVIDER\tLABEL\tKEY\tFLAGS\tTHIS MONTH\tBUDGET\tLAST TEST")
		for _, c := range r.Credentials {
			var flags []string
			if c.IsDefault {
				flags = append(flags, "default")
			}
			if !c.Enabled {
				flags = append(flags, "disabled")
			}
			if c.Fallback {
				flags = append(flags, "fallback")
			}
			month := "-"
			if c.MonthUsage != nil {
				month = fmt.Sprintf("$%.2f · %s tok", c.MonthUsage.CostUSD, fmtTokens(c.MonthUsage.InputTokens+c.MonthUsage.OutputTokens))
			}
			budget := "-"
			if c.MonthlyBudgetUSD > 0 {
				budget = fmt.Sprintf("$%.2f", c.MonthlyBudgetUSD)
				if c.HardStop {
					budget += " (hard stop)"
				}
			}
			test := "never"
			if c.LastTestedAt != nil {
				test = "failed"
				if c.LastTestOK != nil && *c.LastTestOK {
					test = "ok"
				}
				test += " " + c.LastTestedAt.Local().Format("Jan 02")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t…%s\t%s\t%s\t%s\t%s\n", c.ID, c.Provider, c.Label, c.Last4, strings.Join(flags, ","), month, budget, test)
		}
		return w.Flush()
	case "add":
		fs := flag.NewFlagSet("key add", flag.ExitOnError)
		provider := fs.String("provider", "", "claude | openai | gemini | openai_compatible")
		label := fs.String("label", "", "a name for this key, e.g. personal or work")
		base := fs.String("base-url", "", "base URL (OpenAI-compatible servers)")
		def := fs.Bool("default", false, "make this the provider's default key")
		fs.Parse(rest)
		if *provider == "" {
			return errors.New("--provider is required")
		}
		secret, err := readSecret(fmt.Sprintf("API key for %s: ", *provider), *provider == "openai_compatible")
		if err != nil {
			return err
		}
		var c protocol.Credential
		if err := call(protocol.MethodCredentialAdd, protocol.CredentialAddParams{Provider: *provider, Label: *label, Secret: secret, BaseURL: *base, IsDefault: *def}, &c); err != nil {
			return err
		}
		fmt.Printf("Added %s key %q (%s, …%s). Testing…\n", c.Provider, c.Label, c.ID, c.Last4)
		return testKey(c.ID)
	case "test":
		k, err := id()
		if err != nil {
			return err
		}
		return testKey(k)
	case "default":
		k, err := id()
		if err != nil {
			return err
		}
		return update(protocol.CredentialUpdateParams{CredentialID: k, IsDefault: &yes})
	case "enable", "disable":
		k, err := id()
		if err != nil {
			return err
		}
		v := sub == "enable"
		return update(protocol.CredentialUpdateParams{CredentialID: k, Enabled: &v})
	case "fallback":
		if len(rest) < 2 {
			return errors.New("usage: umcode key fallback ID on|off")
		}
		v := &no
		if rest[1] == "on" {
			v = &yes
		}
		return update(protocol.CredentialUpdateParams{CredentialID: rest[0], Fallback: v})
	case "rotate":
		k, err := id()
		if err != nil {
			return err
		}
		secret, err := readSecret("New API key: ", false)
		if err != nil {
			return err
		}
		var c protocol.Credential
		if err := call(protocol.MethodCredentialRotate, protocol.CredentialRotateParams{CredentialID: k, Secret: secret}, &c); err != nil {
			return err
		}
		fmt.Printf("Rotated %q, now …%s\n", c.Label, c.Last4)
		return testKey(k)
	case "delete", "rm":
		k, err := id()
		if err != nil {
			return err
		}
		if err := call(protocol.MethodCredentialDelete, protocol.CredentialIDParams{CredentialID: k}, nil); err != nil {
			return err
		}
		fmt.Println("Deleted. Its usage history is kept.")
		return nil
	case "budget":
		fs := flag.NewFlagSet("key budget", flag.ExitOnError)
		hard := fs.Bool("hard-stop", false, "block requests once the budget is reached")
		if len(rest) < 2 {
			return errors.New("usage: umcode key budget ID USD [--hard-stop]  (0 removes the budget)")
		}
		usd, err := strconv.ParseFloat(rest[1], 64)
		if err != nil {
			return fmt.Errorf("budget: %w", err)
		}
		fs.Parse(rest[2:])
		return call(protocol.MethodUsageSetBudget, protocol.UsageSetBudgetParams{CredentialID: rest[0], MonthlyBudgetUSD: usd, HardStop: *hard}, nil)
	}
	return fmt.Errorf("unknown key command %q", sub)
}

func testKey(id string) error {
	var r protocol.CredentialTestResult
	if err := call(protocol.MethodCredentialTest, protocol.CredentialIDParams{CredentialID: id}, &r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("key test failed after %dms: %s", r.LatencyMs, r.Error)
	}
	fmt.Printf("OK in %dms — %d models available.\n", r.LatencyMs, len(r.Models))
	return nil
}

// readSecret reads a secret without echo when stdin is a terminal.
func readSecret(prompt string, optional bool) (string, error) {
	if v := os.Getenv("UMCODE_KEY"); v != "" {
		return v, nil
	}
	fi, _ := os.Stdin.Stat()
	tty := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	if tty {
		fmt.Fprint(os.Stderr, prompt)
		if err := exec.Command("stty", "-echo").Run(); err == nil {
			defer func() {
				exec.Command("stty", "echo").Run()
				fmt.Fprintln(os.Stderr)
			}()
		}
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" && !optional {
		if err != nil {
			return "", fmt.Errorf("no key entered: %w", err)
		}
		return "", errors.New("no key entered")
	}
	return line, nil
}

// ---- models ----

// errNothingToRoute exits non-zero after the reason has already been printed.
var errNothingToRoute = errors.New("no model is available")

func runModel(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("model list", flag.ExitOnError)
		p := fs.String("p", "", "provider")
		all := fs.Bool("all", false, "include hidden models")
		fs.Parse(rest)
		var r protocol.ModelListResult
		if err := call(protocol.MethodModelList, protocol.ModelListParams{Provider: *p, IncludeHidden: *all}, &r); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "PROVIDER\tMODEL\tNAME\tREASONING\tIN $/M\tCACHED $/M\tOUT $/M\tSOURCE")
		for _, m := range r.Models {
			price := func(v float64) string {
				if m.Source == "api" && v == 0 && m.Provider != "openai_compatible" {
					return "?"
				}
				return fmt.Sprintf("%.2f", v)
			}
			reason := "-"
			if m.SupportsReasoning {
				reason = "yes"
			}
			name := m.DisplayName
			if m.Hidden {
				name += " (hidden)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Provider, m.ID, name, reason,
				price(m.InputPerMTok), price(m.CachedInputPerMTok), price(m.OutputPerMTok), m.Source)
		}
		return w.Flush()
	case "hide", "show":
		if len(rest) < 2 {
			return fmt.Errorf("usage: umcode model %s PROVIDER MODEL", sub)
		}
		return call(protocol.MethodModelSetHidden, protocol.ModelSetHiddenParams{Provider: rest[0], Model: rest[1], Hidden: sub == "hide"}, nil)
	case "price":
		if len(rest) < 5 {
			return errors.New("usage: umcode model price PROVIDER MODEL IN CACHED_IN OUT  (USD per 1M tokens)")
		}
		var v [3]float64
		for i := range v {
			f, err := strconv.ParseFloat(rest[2+i], 64)
			if err != nil {
				return err
			}
			v[i] = f
		}
		return call(protocol.MethodModelSetPrice, protocol.ModelSetPriceParams{Provider: rest[0], Model: rest[1],
			InputPerMTok: v[0], CachedInputPerMTok: v[1], OutputPerMTok: v[2]}, nil)
	case "refresh":
		if len(rest) < 1 {
			return errors.New("usage: umcode model refresh KEY_ID")
		}
		return testKey(rest[0])
	case "configured":
		var r protocol.RoutingConfig
		if err := call(protocol.MethodRoutingGet, nil, &r); err != nil {
			return err
		}
		if len(r.Models) == 0 {
			fmt.Println("No models configured yet; every catalog model is allowed.")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "ID\tNAME\tPROVIDER\tMODEL\tENABLED")
		for _, m := range r.Models {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", m.ID, m.Name, m.Provider, m.Model, m.Enabled)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if len(r.Pools) == 0 {
			return nil
		}
		fmt.Println()
		w = table()
		fmt.Fprintln(w, "POOL\tNAME\tSTRATEGY\tMODELS\tDEFAULT")
		for _, p := range r.Pools {
			def := ""
			if p.ID == r.DefaultPool {
				def = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Strategy, strings.Join(p.Models, ", "), def)
		}
		return w.Flush()
	case "route":
		fs := flag.NewFlagSet("model route", flag.ExitOnError)
		complexity := fs.String("c", "", "quick | standard | deep")
		provider := fs.String("p", "", "pin a provider")
		model := fs.String("m", "", "pin a model")
		key := fs.String("k", "", "pin an API key")
		pool := fs.String("pool", "", "route within this pool")
		fs.Parse(rest)
		var r protocol.ModelRouteResult
		if err := call(protocol.MethodModelRoute, protocol.ModelRouteParams{
			Complexity: protocol.Complexity(*complexity), Text: strings.Join(fs.Args(), " "), Pool: *pool,
			Override: protocol.ModelSelection{Provider: *provider, Model: *model, CredentialID: *key},
		}, &r); err != nil {
			return err
		}
		if r.Chosen == nil {
			reason := r.Reason
			if reason == "" {
				reason = "no model is available"
			}
			fmt.Printf("Nothing can serve a message right now: %s\n", reason)
			if len(r.Alternatives) == 0 {
				return errNothingToRoute
			}
			w := table()
			fmt.Fprintln(w, "PROVIDER\tMODEL\tKEY\tWHY NOT")
			for _, rt := range r.Alternatives {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", rt.Provider, rt.Model, rt.CredentialL, rt.Unavailable)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			return errNothingToRoute
		}
		auto := ""
		if r.AutoPicked {
			auto = " (picked by Auto)"
		}
		fmt.Printf("complexity: %s%s\n\n", r.Complexity, auto)
		w := table()
		fmt.Fprintln(w, "ORDER	PROVIDER	MODEL	KEY	WHY	$/M	STATUS")
		rows := append([]protocol.RouteInfo{*r.Chosen}, r.Alternatives...)
		for i, rt := range rows {
			order, status := fmt.Sprintf("%d", i+1), "ready"
			if i == 0 {
				order = "now"
			}
			if rt.Unavailable != "" {
				order, status = "-", rt.Unavailable
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.2f\t%s\n", order, rt.Provider, rt.Model,
				rt.CredentialL, rt.Why, rt.CostPerMTok, status)
		}
		return w.Flush()
	case "health":
		fs := flag.NewFlagSet("model health", flag.ExitOnError)
		clear := fs.Bool("clear", false, "clear the cooldown on PROVIDER MODEL KEY_ID")
		fs.Parse(rest)
		var p protocol.ModelHealthParams
		if *clear {
			if fs.NArg() < 3 {
				return errors.New("usage: umcode model health -clear PROVIDER MODEL KEY_ID")
			}
			p.Clear = &protocol.RouteRef{Provider: fs.Arg(0), Model: fs.Arg(1), CredentialID: fs.Arg(2)}
		}
		var r protocol.ModelHealthResult
		if err := call(protocol.MethodModelHealth, p, &r); err != nil {
			return err
		}
		if len(r.Rows) == 0 {
			fmt.Println("Nothing has failed yet.")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "PROVIDER\tMODEL\tKEY\tOK\tERR\tLATENCY\tCOOLING DOWN\tLAST")
		for _, row := range r.Rows {
			cool := "-"
			if row.CooldownEnd != nil {
				cool = "until " + row.CooldownEnd.Local().Format("15:04:05")
			}
			last := row.LastStatus
			if last == "" {
				last = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%dms\t%s\t%s\n", row.Provider, row.Model, row.Label,
				row.OKCount, row.ErrCount, row.LatencyMs, cool, last)
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown model command %q", sub)
}

func runComplexity(args []string) error {
	if len(args) >= 2 && args[0] == "default" {
		var d protocol.ComplexityDefaults
		if err := call(protocol.MethodComplexityGetDefaults, nil, &d); err != nil {
			return err
		}
		d.Default = protocol.Complexity(args[1])
		return call(protocol.MethodComplexitySetDefaults, d, nil)
	}
	var d protocol.ComplexityDefaults
	if err := call(protocol.MethodComplexityGetDefaults, nil, &d); err != nil {
		return err
	}
	fmt.Printf("default: %s\n\n", d.Default)
	w := table()
	fmt.Fprintln(w, "LEVEL\tREASONING\tMAX TOOL STEPS\tMULTI-AGENT\tMAX OUTPUT")
	for _, p := range d.Presets {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\n", p.Level, p.Reasoning, p.MaxToolSteps, p.MultiAgent, p.MaxOutputTokens)
	}
	return w.Flush()
}

// ---- usage ----

func runUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	by := fs.String("by", "credential", "credential | model | thread | role | day")
	days := fs.Int("days", 30, "look back this many days")
	key := fs.String("key", "", "only this API key")
	fs.Parse(args)
	to := time.Now().Add(time.Minute)
	var r protocol.UsageSummaryResult
	if err := call(protocol.MethodUsageSummary, protocol.UsageSummaryParams{GroupBy: *by, From: to.AddDate(0, 0, -*days), To: to, CredentialID: *key}, &r); err != nil {
		return err
	}
	w := table()
	fmt.Fprintf(w, "%s\tREQUESTS\tINPUT\tCACHED\tOUTPUT\tREASONING\tCOST\n", strings.ToUpper(*by))
	for _, row := range r.Rows {
		u := row.Usage
		label := or(row.Label, row.Key)
		if u.Estimated {
			label += " *"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t$%.4f\n", clip(label, 50), u.Requests, fmtTokens(u.InputTokens),
			fmtTokens(u.CachedInputTokens), fmtTokens(u.OutputTokens), fmtTokens(u.ReasoningTokens), u.CostUSD)
	}
	t := r.Total
	fmt.Fprintf(w, "TOTAL\t%d\t%s\t%s\t%s\t%s\t$%.4f\n", t.Requests, fmtTokens(t.InputTokens), fmtTokens(t.CachedInputTokens),
		fmtTokens(t.OutputTokens), fmtTokens(t.ReasoningTokens), t.CostUSD)
	if err := w.Flush(); err != nil {
		return err
	}
	if t.Estimated {
		fmt.Println("* includes estimated tokens (the provider did not report usage)")
	}
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
