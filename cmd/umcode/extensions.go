package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tasks"
)

// ---- plugins ----

func runPlugin(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "inspect":
		if len(rest) != 1 {
			return errors.New("usage: umcode plugin inspect SOURCE")
		}
		var inspection protocol.PluginInspection
		if err := call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: rest[0]}, &inspection); err != nil {
			return err
		}
		fmt.Printf("%s %s (%s)\n", inspection.Plugin.Name, or(inspection.Plugin.Version, "unversioned"), inspection.Plugin.Format)
		fmt.Printf("Source: %s\nMode: %s\nComponents: %d\nRequires approval: %t\nToken: %s\n", inspection.Source, inspection.Mode, len(inspection.Plugin.Components), inspection.RequiresApproval, inspection.Token)
		return nil
	case "install":
		source, projectID, linked, err := parsePluginInstallArgs(rest)
		if err != nil {
			return errors.New("usage: umcode plugin install [--linked] [--project ID] SOURCE")
		}
		mode := "managed"
		if linked {
			mode = "linked"
		}
		var inspection protocol.PluginInspection
		if err := call(protocol.MethodPluginInspect, protocol.PluginInspectParams{Source: source, Mode: mode}, &inspection); err != nil {
			return err
		}
		var installed protocol.PluginInfo
		if err := call(protocol.MethodPluginInstall, protocol.PluginInstallParams{Token: inspection.Token, ProjectID: projectID}, &installed); err != nil {
			return err
		}
		fmt.Printf("Installed %s (%s) as %s.\n", installed.Name, or(installed.Version, "unversioned"), installed.ID)
		return nil
	case "list", "ls":
		fs := flag.NewFlagSet("plugin list", flag.ExitOnError)
		projectID := fs.String("project", "", "show enablement for this project")
		fs.Parse(rest)
		var result protocol.PluginListResult
		if err := call(protocol.MethodPluginList, protocol.PluginListParams{ProjectID: *projectID}, &result); err != nil {
			return err
		}
		if len(result.Plugins) == 0 {
			fmt.Println("No plugins installed.")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "ID\tNAME\tVERSION\tFORMAT\tMODE\tENABLED\tCOMPONENTS")
		for _, plugin := range result.Plugins {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%d\n", plugin.ID, plugin.Name, or(plugin.Version, "-"), plugin.Format, plugin.Mode, plugin.Enabled, len(plugin.Components))
		}
		return w.Flush()
	case "show":
		if len(rest) != 1 {
			return errors.New("usage: umcode plugin show ID")
		}
		var plugin protocol.PluginInfo
		if err := call(protocol.MethodPluginGet, protocol.PluginGetParams{PluginID: rest[0]}, &plugin); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(plugin, "", "  ")
		fmt.Println(string(data))
		return nil
	case "enable", "disable":
		pluginID, projectID, err := parsePluginProjectArgs(rest)
		if err != nil {
			return fmt.Errorf("usage: umcode plugin %s --project ID PLUGIN_ID", sub)
		}
		var plugin protocol.PluginInfo
		if err := call(protocol.MethodPluginSetEnabled, protocol.PluginSetEnabledParams{PluginID: pluginID, ProjectID: projectID, Enabled: sub == "enable"}, &plugin); err != nil {
			return err
		}
		verb := "Enabled"
		if sub == "disable" {
			verb = "Disabled"
		}
		fmt.Printf("%s %s for project %s.\n", verb, plugin.Name, projectID)
		return nil
	case "reload":
		if len(rest) != 1 {
			return errors.New("usage: umcode plugin reload ID")
		}
		var plugin protocol.PluginInfo
		if err := call(protocol.MethodPluginReload, protocol.PluginIDParams{PluginID: rest[0]}, &plugin); err != nil {
			return err
		}
		fmt.Printf("Reloaded %s (%s).\n", plugin.Name, or(plugin.Version, "unversioned"))
		return nil
	case "remove", "rm", "uninstall":
		pluginID, disable, err := parsePluginRemoveArgs(rest)
		if err != nil {
			return errors.New("usage: umcode plugin remove [--disable-projects] ID")
		}
		if err := call(protocol.MethodPluginUninstall, protocol.PluginUninstallParams{PluginID: pluginID, DisableProjects: disable}, nil); err != nil {
			return err
		}
		fmt.Println("Removed.")
		return nil
	default:
		return fmt.Errorf("unknown plugin command %q", sub)
	}
}

func parsePluginInstallArgs(args []string) (source, projectID string, linked bool, err error) {
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--linked":
			linked = true
		case args[index] == "--project" && index+1 < len(args):
			index++
			projectID = args[index]
		case strings.HasPrefix(args[index], "--project="):
			projectID = strings.TrimPrefix(args[index], "--project=")
		case strings.HasPrefix(args[index], "-") || source != "":
			return "", "", false, errors.New("invalid plugin install arguments")
		default:
			source = args[index]
		}
	}
	if source == "" {
		err = errors.New("source is required")
	}
	return
}

func parsePluginProjectArgs(args []string) (pluginID, projectID string, err error) {
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--project" && index+1 < len(args):
			index++
			projectID = args[index]
		case strings.HasPrefix(args[index], "--project="):
			projectID = strings.TrimPrefix(args[index], "--project=")
		case strings.HasPrefix(args[index], "-") || pluginID != "":
			return "", "", errors.New("invalid plugin project arguments")
		default:
			pluginID = args[index]
		}
	}
	if pluginID == "" || projectID == "" {
		err = errors.New("plugin and project are required")
	}
	return
}

func parsePluginRemoveArgs(args []string) (pluginID string, disable bool, err error) {
	for _, arg := range args {
		switch {
		case arg == "--disable-projects":
			disable = true
		case strings.HasPrefix(arg, "-") || pluginID != "":
			return "", false, errors.New("invalid plugin remove arguments")
		default:
			pluginID = arg
		}
	}
	if pluginID == "" {
		err = errors.New("plugin is required")
	}
	return
}

// ---- tasks ----

func runTask(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	taskID := func() (int64, error) {
		if len(rest) < 1 {
			return 0, fmt.Errorf("usage: umcode task %s ID", sub)
		}
		return strconv.ParseInt(strings.TrimPrefix(rest[0], "#"), 10, 64)
	}
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("task list", flag.ExitOnError)
		all := fs.Bool("all", false, "include completed and cancelled tasks")
		fs.Parse(rest)
		status := protocol.TaskActive
		if *all {
			status = ""
		}
		var r protocol.TaskListResult
		if err := call(protocol.MethodTaskList, protocol.TaskListParams{Status: status}, &r); err != nil {
			return err
		}
		if len(r.Tasks) == 0 {
			fmt.Println("No tasks. Create one with: umcode task add --daily 09:00 \"summarise my inbox\"")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "ID\tNAME\tSCHEDULE\tSTATUS\tNEXT RUN\tLAST RESULT")
		for _, t := range r.Tasks {
			next := "-"
			if t.NextRunAt != nil {
				next = t.NextRunAt.Local().Format("Mon Jan 02 15:04")
			}
			last := t.LastResult
			if t.LastError != "" {
				last = "error: " + t.LastError
			}
			sched := tasks.Describe(protocol.TaskCreateParams{TaskType: t.TaskType, Schedule: t.Schedule})
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.ID, clip(t.Name, 30), sched, t.Status, next,
				clip(strings.ReplaceAll(last, "\n", " "), 50))
		}
		return w.Flush()
	case "add", "create":
		fs := flag.NewFlagSet("task add", flag.ExitOnError)
		name := fs.String("name", "", "task name (default: start of the prompt)")
		at := fs.String("at", "", "run once at this local time, e.g. 2026-09-20T09:00")
		daily := fs.String("daily", "", "run every day at HH:MM")
		weekly := fs.String("weekly", "", "run every week: DAY@HH:MM, e.g. mon@09:00")
		hourly := fs.Int("hourly", -1, "run every hour at this minute")
		cron := fs.String("cron", "", "cron expression, e.g. \"0 9 * * mon-fri\"")
		tz := fs.String("tz", "", "IANA timezone (default: local)")
		p := fs.String("p", "", "provider")
		m := fs.String("m", "", "model")
		c := fs.String("c", "", "complexity")
		fs.Parse(rest)
		prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if prompt == "" {
			return errors.New("usage: umcode task add (--at T | --daily HH:MM | --weekly DAY@HH:MM | --hourly M | --cron EXPR) PROMPT")
		}
		params := protocol.TaskCreateParams{Name: *name, Prompt: prompt, Timezone: *tz, TaskType: protocol.TaskPeriodic,
			Settings: protocol.ModelSelection{Provider: *p, Model: *m, Complexity: protocol.Complexity(*c)}}
		switch {
		case *at != "":
			params.TaskType, params.Schedule = protocol.TaskOneTime, protocol.Schedule{RunAt: *at}
		case *daily != "":
			params.Schedule = protocol.Schedule{Frequency: "daily", Time: *daily}
		case *weekly != "":
			day, hm, ok := strings.Cut(*weekly, "@")
			if !ok {
				return errors.New("--weekly takes DAY@HH:MM, e.g. mon@09:00")
			}
			params.Schedule = protocol.Schedule{Frequency: "weekly", DayOfWeek: day, Time: hm}
		case *hourly >= 0:
			params.Schedule = protocol.Schedule{Frequency: "hourly", Minute: hourly}
		case *cron != "":
			params.Schedule = protocol.Schedule{Frequency: "cron", Cron: *cron}
		default:
			return errors.New("choose a schedule: --at, --daily, --weekly, --hourly or --cron")
		}
		var t protocol.Task
		if err := call(protocol.MethodTaskCreate, params, &t); err != nil {
			return err
		}
		fmt.Printf("Created task #%d %q; next run %s.\n", t.ID, t.Name, t.NextRunAt.Local().Format("Mon Jan 02 15:04 MST"))
		return nil
	case "cancel", "rm", "delete":
		id, err := taskID()
		if err != nil {
			return err
		}
		var t protocol.Task
		if err := call(protocol.MethodTaskCancel, protocol.TaskIDParams{TaskID: id}, &t); err != nil {
			return err
		}
		fmt.Printf("Cancelled task #%d %q.\n", t.ID, t.Name)
		return nil
	case "run":
		id, err := taskID()
		if err != nil {
			return err
		}
		if err := call(protocol.MethodTaskRunNow, protocol.TaskIDParams{TaskID: id}, nil); err != nil {
			return err
		}
		fmt.Printf("Task #%d will run within a few seconds; see `umcode task runs %d`.\n", id, id)
		return nil
	case "runs":
		id, err := taskID()
		if err != nil {
			return err
		}
		var r protocol.TaskRunsResult
		if err := call(protocol.MethodTaskRuns, protocol.TaskIDParams{TaskID: id}, &r); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "RUN\tSTARTED\tSTATUS\tTURN\tRESULT")
		for _, run := range r.Runs {
			res := run.Result
			if run.Error != "" {
				res = "error: " + run.Error
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", run.ID, run.StartedAt.Local().Format("Jan 02 15:04"), run.Status,
				or(run.TurnID, "-"), clip(strings.ReplaceAll(res, "\n", " "), 70))
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown task command %q", sub)
}

// ---- skills ----

func runSkill(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		var r protocol.SkillListResult
		if err := call(protocol.MethodSkillList, nil, &r); err != nil {
			return err
		}
		if len(r.Skills) == 0 {
			fmt.Printf("No skills installed. Looked in: %s\n", strings.Join(r.Dirs, ", "))
			return nil
		}
		w := table()
		fmt.Fprintln(w, "NAME\tRUNTIME\tRISK\tSCRIPTS\tDESCRIPTION")
		for _, s := range r.Skills {
			if s.Error != "" {
				fmt.Fprintf(w, "%s\t-\t-\t-\t%sinvalid: %s%s\n", s.Name, red, s.Error, reset)
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Runtime, s.RiskLevel, strings.Join(s.Scripts, ","), clip(s.Description, 60))
		}
		return w.Flush()
	case "show":
		if len(rest) < 1 {
			return errors.New("usage: umcode skill show NAME")
		}
		var r protocol.SkillGetResult
		if err := call(protocol.MethodSkillGet, protocol.SkillNameParams{Name: rest[0]}, &r); err != nil {
			return err
		}
		fmt.Printf("%s%s%s  %s(%s)%s\n%s\n\n%s\n", bold, r.Skill.Name, reset, dim, r.Skill.Dir, reset, r.Skill.Description, r.Instructions)
		return nil
	case "install", "add":
		fs := flag.NewFlagSet("skill install", flag.ExitOnError)
		name := fs.String("name", "", "install under a different name")
		fs.Parse(rest)
		if fs.NArg() < 1 {
			return errors.New("usage: umcode skill install PATH_OR_GIT_URL [--name NAME]")
		}
		var s protocol.SkillInfo
		if err := call(protocol.MethodSkillInstall, protocol.SkillInstallParams{Source: fs.Arg(0), Name: *name}, &s); err != nil {
			return err
		}
		fmt.Printf("Installed %s into %s\n", s.Name, s.Dir)
		return nil
	case "remove", "rm":
		if len(rest) < 1 {
			return errors.New("usage: umcode skill remove NAME")
		}
		if err := call(protocol.MethodSkillRemove, protocol.SkillNameParams{Name: rest[0]}, nil); err != nil {
			return err
		}
		fmt.Println("Removed.")
		return nil
	}
	return fmt.Errorf("unknown skill command %q", sub)
}

// ---- MCP and tools ----

func runMCP(args []string) error {
	if len(args) >= 2 && args[0] == "restart" {
		if err := call(protocol.MethodMCPRestart, protocol.MCPRestartParams{Name: args[1]}, nil); err != nil {
			return err
		}
		fmt.Println("Restarted.")
		return nil
	}
	var r protocol.MCPListResult
	if err := call(protocol.MethodMCPList, nil, &r); err != nil {
		return err
	}
	if len(r.Servers) == 0 {
		fmt.Println("No MCP servers configured (mcp_servers in config.yaml).")
		return nil
	}
	w := table()
	fmt.Fprintln(w, "NAME\tTRANSPORT\tSTATUS\tSERVER\tTOOLS")
	for _, s := range r.Servers {
		status := s.Status
		if s.Error != "" {
			status += ": " + clip(s.Error, 60)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", s.Name, s.Transport, status, strings.TrimSpace(s.ServerName+" "+s.ServerVersion), len(s.Tools))
	}
	return w.Flush()
}

func runTools() error {
	var r protocol.ToolListResult
	if err := call(protocol.MethodToolList, nil, &r); err != nil {
		return err
	}
	w := table()
	fmt.Fprintln(w, "TOOL\tSOURCE\tDESCRIPTION")
	for _, t := range r.Tools {
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.Source, clip(t.Description, 80))
	}
	return w.Flush()
}
