package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
)

// runProject implements `umcode project …`.
func runProject(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("project list", flag.ExitOnError)
		all := fs.Bool("all", false, "include archived projects")
		fs.Parse(rest)
		var r protocol.ProjectListResult
		if err := call(protocol.MethodProjectList, protocol.ProjectListParams{IncludeArchived: *all}, &r); err != nil {
			return err
		}
		if len(r.Projects) == 0 {
			fmt.Println("No projects yet. Add one with: umcode project add ~/code/my-app")
			return nil
		}
		w := table()
		fmt.Fprintln(w, "ID\tNAME\tROOT\tCHATS\tBRANCH\tSTATE")
		for _, p := range r.Projects {
			branch, state := "-", "ok"
			if p.VCS != nil && p.VCS.Branch != "" {
				branch = p.VCS.Branch
				if p.VCS.Dirty > 0 {
					branch = fmt.Sprintf("%s (%d changed)", branch, p.VCS.Dirty)
				}
			}
			switch {
			case p.Missing:
				state = "folder missing"
			case p.Archived:
				state = "archived"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", p.ID, clip(p.Name, 24), clip(home(p.Root), 44), p.Threads, branch, state)
		}
		return w.Flush()

	case "add", "create":
		fs := flag.NewFlagSet("project add", flag.ExitOnError)
		name := fs.String("name", "", "display name (default: the folder's name)")
		model := fs.String("m", "", "default model for chats in this project")
		provider := fs.String("p", "", "default provider")
		cplx := fs.String("c", "", "default complexity: auto|quick|standard|deep")
		compute := fs.Bool("compute", false, "run commands in the isolated microVM")
		network := fs.Bool("network", false, "allow commands that use the network")
		fs.Parse(rest)
		root := ""
		if rest := fs.Args(); len(rest) > 0 {
			// Allow flags on either side of the path: `add PATH --name x`.
			root = strings.TrimSpace(rest[0])
			if len(rest) > 1 {
				fs.Parse(rest[1:])
			}
		}
		if root == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root = cwd
		}
		abs, err := projects.ExpandPath(root)
		if err != nil {
			return err
		}
		var p protocol.Project
		if err := call(protocol.MethodProjectCreate, protocol.ProjectCreateParams{
			Root: abs, Name: *name,
			Settings: protocol.ModelSelection{Provider: *provider, Model: *model, Complexity: protocol.Complexity(*cplx)},
			Tools:    protocol.ProjectTools{Compute: compute, Network: network},
		}, &p); err != nil {
			return err
		}
		fmt.Printf("Added project %s (%s)\nChat in it with: umcode chat --project %s \"…\"\n", p.Name, p.ID, p.ID)
		return nil

	case "show", "info":
		id, err := projectArg(rest, "show")
		if err != nil {
			return err
		}
		var p protocol.Project
		if err := call(protocol.MethodProjectOpen, protocol.ProjectIDParams{ProjectID: id}, &p); err != nil {
			return err
		}
		fmt.Printf("%s%s%s  %s\n", bold, p.Name, reset, p.ID)
		fmt.Printf("  folder   %s%s\n", p.Root, missingNote(p))
		if p.VCS != nil {
			fmt.Printf("  git      %s (%d changed files)\n", p.VCS.Branch, p.VCS.Dirty)
		}
		fmt.Printf("  chats    %d\n", p.Threads)
		fmt.Printf("  model    %s\n", describeSelection(p.Settings))
		fmt.Printf("  compute  %s, network %s\n", onOff(p.Tools.Compute, false), onOff(p.Tools.Network, false))
		var ins protocol.ProjectInstructionsResult
		if err := call(protocol.MethodProjectInstructions, protocol.ProjectInstructionsParams{ProjectID: id}, &ins); err == nil {
			fmt.Printf("  AGENT.md %s\n", ins.Path)
			for _, s := range ins.Sources {
				fmt.Printf("           %s: %s (%d bytes)%s\n", s.Scope, home(s.Path), s.Bytes, note(s.Error))
			}
		}
		return nil

	case "instructions", "agent", "agentmd":
		id, err := projectArg(rest, "instructions")
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("project instructions", flag.ExitOnError)
		edit := fs.String("f", "", "write the contents of this file as the project's AGENT.md ('-' for stdin)")
		composed := fs.Bool("composed", false, "print the full instructions the agent receives")
		fs.Parse(rest[1:])
		params := protocol.ProjectInstructionsParams{ProjectID: id}
		if *edit != "" {
			data, err := readFileOrStdin(*edit)
			if err != nil {
				return err
			}
			text := string(data)
			params.Content = &text
		}
		var ins protocol.ProjectInstructionsResult
		if err := call(protocol.MethodProjectInstructions, params, &ins); err != nil {
			return err
		}
		if *composed {
			fmt.Print(ins.Composed)
			return nil
		}
		if params.Content != nil {
			fmt.Printf("Wrote %s\n", ins.Path)
			return nil
		}
		if strings.TrimSpace(ins.Project) == "" {
			fmt.Printf("No project instructions yet. Create %s, or: umcode project instructions %s -f notes.md\n", ins.Path, id)
			return nil
		}
		fmt.Print(ins.Project)
		return nil

	case "files", "ls-files":
		id, err := projectArg(rest, "files")
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("project files", flag.ExitOnError)
		depth := fs.Int("depth", 1, "levels to list")
		fs.Parse(rest[1:])
		path := ""
		if args := fs.Args(); len(args) > 0 {
			path = args[0]
			if len(args) > 1 {
				fs.Parse(args[1:])
			}
		}
		var r protocol.ProjectFilesResult
		if err := call(protocol.MethodProjectFiles, protocol.ProjectFilesParams{ProjectID: id, Path: path, Depth: *depth}, &r); err != nil {
			return err
		}
		for _, e := range r.Entries {
			kind := "file"
			if e.Dir {
				kind = "dir "
			}
			fmt.Printf("%s  %s\n", kind, e.Path)
		}
		if r.Truncated {
			fmt.Println("… more files not shown")
		}
		return nil

	case "diff":
		fs := flag.NewFlagSet("project diff", flag.ExitOnError)
		turn := fs.String("turn", "", "only what this turn changed")
		fs.Parse(rest)
		id := ""
		if args := fs.Args(); len(args) > 0 {
			id = args[0]
			if len(args) > 1 {
				fs.Parse(args[1:])
			}
		}
		if id == "" && *turn == "" {
			return fmt.Errorf("usage: umcode project diff PROJECT_ID [--turn TURN_ID]")
		}
		var r protocol.ProjectDiffResult
		if err := call(protocol.MethodProjectDiff, protocol.ProjectDiffParams{ProjectID: id, TurnID: *turn}, &r); err != nil {
			return err
		}
		if len(r.Files) == 0 {
			fmt.Println("No changes recorded.")
			return nil
		}
		for _, f := range r.Files {
			fmt.Printf("%s%s%s  %s (+%d −%d)%s\n", bold, f.Path, reset, f.Action, f.Additions, f.Deletions, turnNote(f))
			if f.Diff != "" {
				fmt.Println(f.Diff)
			}
		}
		return nil

	case "revert":
		if len(rest) < 1 {
			return fmt.Errorf("usage: umcode project revert TURN_ID [PATH…]")
		}
		var r protocol.ProjectRevertTurnResult
		if err := call(protocol.MethodProjectRevertTurn, protocol.ProjectRevertTurnParams{TurnID: rest[0], Paths: rest[1:]}, &r); err != nil {
			return err
		}
		if len(r.Reverted) == 0 {
			fmt.Println("Nothing was reverted.")
		} else {
			fmt.Printf("Reverted %s\n", strings.Join(r.Reverted, ", "))
		}
		if r.Reason != "" {
			fmt.Println(r.Reason)
		}
		return nil

	case "set":
		id, err := projectArg(rest, "set")
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("project set", flag.ExitOnError)
		name := fs.String("name", "", "display name")
		provider := fs.String("p", "", "default provider")
		model := fs.String("m", "", "default model")
		cplx := fs.String("c", "", "default complexity")
		key := fs.String("k", "", "default API key id")
		compute := fs.String("compute", "", "on|off: run commands in the isolated microVM")
		network := fs.String("network", "", "on|off: allow commands that use the network")
		fs.Parse(rest[1:])
		params := protocol.ProjectUpdateParams{ProjectID: id}
		if *name != "" {
			params.Name = name
		}
		if *provider != "" || *model != "" || *cplx != "" || *key != "" {
			params.Settings = &protocol.ModelSelection{Provider: *provider, Model: *model,
				Complexity: protocol.Complexity(*cplx), CredentialID: *key}
		}
		if *compute != "" || *network != "" {
			tools := protocol.ProjectTools{}
			if v, err := onOffFlag(*compute); err == nil && *compute != "" {
				tools.Compute = v
			}
			if v, err := onOffFlag(*network); err == nil && *network != "" {
				tools.Network = v
			}
			params.Tools = &tools
		}
		var p protocol.Project
		if err := call(protocol.MethodProjectUpdate, params, &p); err != nil {
			return err
		}
		fmt.Printf("Updated %s\n", p.Name)
		return nil

	case "archive", "unarchive":
		id, err := projectArg(rest, sub)
		if err != nil {
			return err
		}
		archived := sub == "archive"
		var p protocol.Project
		if err := call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: id, Archived: &archived}, &p); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", map[bool]string{true: "Archived", false: "Restored"}[archived], p.Name)
		return nil

	case "remove", "rm", "delete":
		id, err := projectArg(rest, "remove")
		if err != nil {
			return err
		}
		if err := call(protocol.MethodProjectDelete, protocol.ProjectIDParams{ProjectID: id}, nil); err != nil {
			return err
		}
		fmt.Println("Removed the project. Its folder and files are untouched.")
		return nil
	}
	return fmt.Errorf("unknown project command %q (list, add, show, instructions, files, diff, revert, set, archive, remove)", sub)
}

// describeSelection renders a project's default model choice.
func describeSelection(sel protocol.ModelSelection) string {
	parts := []string{}
	if sel.Provider != "" {
		parts = append(parts, sel.Provider)
	}
	if sel.Model != "" {
		parts = append(parts, sel.Model)
	}
	if sel.Complexity != "" {
		parts = append(parts, string(sel.Complexity))
	}
	if sel.CredentialID != "" {
		parts = append(parts, "key "+sel.CredentialID)
	}
	if len(parts) == 0 {
		return "engine defaults"
	}
	return strings.Join(parts, " · ")
}

// home shortens a path under the user's home folder for display.
func home(path string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" || !strings.HasPrefix(path, h) {
		return path
	}
	return "~" + strings.TrimPrefix(path, h)
}

func readAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }

// turnNote shows which turn to pass to `umcode project revert`.
func turnNote(f protocol.FileChangeData) string {
	if f.TurnID == "" || !f.Revertable {
		return ""
	}
	return fmt.Sprintf("  %sundo: umcode project revert %s%s", dim, f.TurnID, reset)
}

func projectArg(rest []string, sub string) (string, error) {
	if len(rest) < 1 || strings.HasPrefix(rest[0], "-") {
		return "", fmt.Errorf("usage: umcode project %s PROJECT_ID", sub)
	}
	return rest[0], nil
}

func onOff(p *bool, def bool) string {
	v := def
	if p != nil {
		v = *p
	}
	if v {
		return "on"
	}
	return "off"
}

func onOffFlag(s string) (*bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		t := true
		return &t, nil
	case "off", "false", "no", "0":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("expected on or off, got %q", s)
}

func missingNote(p protocol.Project) string {
	if p.Missing {
		return "  (the folder is gone)"
	}
	return ""
}

func note(s string) string {
	if s == "" {
		return ""
	}
	return "  — " + s
}

func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return readAll(os.Stdin)
	}
	return os.ReadFile(path)
}
