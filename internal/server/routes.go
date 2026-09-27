package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/skills"
)

type handler func(ctx context.Context, c *conn, params json.RawMessage) (any, error)

// bind decodes params into P and calls fn.
func bind[P any](fn func(ctx context.Context, c *conn, p P) (any, error)) handler {
	return func(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "invalid params: %v", err)
			}
		}
		return fn(ctx, c, p)
	}
}

type empty struct{}

type okResult struct {
	OK bool `json:"ok"`
}

func (s *Server) routes() map[string]handler {
	e := s.eng
	return map[string]handler{
		protocol.MethodInitialize: bind(func(ctx context.Context, c *conn, p protocol.InitializeParams) (any, error) {
			if major(p.ProtocolVersion) != major(protocol.Version) {
				return nil, protocol.Errorf(protocol.CodeVersionMismatch,
					"client speaks protocol %s, engine speaks %s", p.ProtocolVersion, protocol.Version)
			}
			c.admin.Store(p.Admin)
			c.initialized.Store(true)
			e.Bus.Add(c)
			return protocol.InitializeResult{EngineVersion: engineVersion(), ProtocolVersion: protocol.Version, ClientID: c.id}, nil
		}),
		protocol.MethodEngineStatus: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			return e.Status(ctx), nil
		}),
		protocol.MethodEventsSubscribe: bind(func(ctx context.Context, c *conn, p protocol.SubscribeParams) (any, error) {
			c.subscribe(p)
			return okResult{true}, nil
		}),

		// threads
		protocol.MethodThreadStart: bind(func(ctx context.Context, c *conn, p protocol.ThreadStartParams) (any, error) {
			t, err := e.StartThread(ctx, p)
			if err == nil {
				c.follow(t.ID)
			}
			return t, err
		}),
		protocol.MethodThreadList: bind(func(ctx context.Context, c *conn, p protocol.ThreadListParams) (any, error) {
			ts, next, err := e.Store.ListThreads(ctx, p)
			if ts == nil {
				ts = []protocol.Thread{}
			}
			return protocol.ThreadListResult{Threads: ts, NextCursor: next}, err
		}),
		protocol.MethodThreadRead: bind(func(ctx context.Context, c *conn, p protocol.ThreadIDParams) (any, error) {
			r, err := e.ReadThread(ctx, p.ThreadID)
			if err == nil {
				c.follow(p.ThreadID)
			}
			return r, err
		}),
		protocol.MethodThreadRename: bind(func(ctx context.Context, c *conn, p protocol.ThreadRenameParams) (any, error) {
			title := strings.TrimSpace(p.Title)
			if title == "" {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "title is empty")
			}
			return e.UpdateThread(ctx, p.ThreadID, map[string]any{"title": title})
		}),
		protocol.MethodThreadPin: bind(func(ctx context.Context, c *conn, p protocol.ThreadFlagParams) (any, error) {
			return e.UpdateThread(ctx, p.ThreadID, map[string]any{"pinned": p.Value})
		}),
		protocol.MethodThreadArchive: bind(func(ctx context.Context, c *conn, p protocol.ThreadFlagParams) (any, error) {
			return e.UpdateThread(ctx, p.ThreadID, map[string]any{"archived": p.Value})
		}),
		protocol.MethodThreadDelete: bind(func(ctx context.Context, c *conn, p protocol.ThreadIDParams) (any, error) {
			return okResult{true}, e.DeleteThread(ctx, p.ThreadID)
		}),
		protocol.MethodThreadFork: bind(func(ctx context.Context, c *conn, p protocol.ThreadForkParams) (any, error) {
			t, err := e.ForkThread(ctx, p)
			if err == nil {
				c.follow(t.ID)
			}
			return t, err
		}),
		protocol.MethodThreadSearch: bind(func(ctx context.Context, c *conn, p protocol.ThreadSearchParams) (any, error) {
			hits, err := e.Store.SearchItems(ctx, p.Query, p.Limit)
			if hits == nil {
				hits = []protocol.SearchHit{}
			}
			return protocol.ThreadSearchResult{Hits: hits}, err
		}),
		protocol.MethodThreadExport: bind(func(ctx context.Context, c *conn, p protocol.ThreadIDParams) (any, error) {
			md, err := e.ExportThread(ctx, p.ThreadID)
			return protocol.ThreadExportResult{Markdown: md}, err
		}),
		protocol.MethodThreadSetSettings: bind(func(ctx context.Context, c *conn, p protocol.ThreadSetSettingsParams) (any, error) {
			return e.SetThreadSettings(ctx, p)
		}),
		protocol.MethodThreadCompact: bind(func(ctx context.Context, c *conn, p protocol.ThreadIDParams) (any, error) {
			return okResult{true}, e.CompactThread(ctx, p.ThreadID)
		}),

		// projects
		protocol.MethodProjectList: bind(func(ctx context.Context, c *conn, p protocol.ProjectListParams) (any, error) {
			list, err := e.Projects.List(ctx, p.IncludeArchived)
			if list == nil {
				list = []protocol.Project{}
			}
			return protocol.ProjectListResult{Projects: list}, err
		}),
		protocol.MethodProjectCreate: bind(func(ctx context.Context, c *conn, p protocol.ProjectCreateParams) (any, error) {
			return e.CreateProject(ctx, p)
		}),
		protocol.MethodProjectOpen: bind(func(ctx context.Context, c *conn, p protocol.ProjectIDParams) (any, error) {
			return e.OpenProject(ctx, p.ProjectID)
		}),
		protocol.MethodProjectUpdate: bind(func(ctx context.Context, c *conn, p protocol.ProjectUpdateParams) (any, error) {
			return e.UpdateProject(ctx, p)
		}),
		protocol.MethodProjectDelete: bind(func(ctx context.Context, c *conn, p protocol.ProjectIDParams) (any, error) {
			return okResult{true}, e.DeleteProject(ctx, p.ProjectID)
		}),
		protocol.MethodProjectInstructions: bind(func(ctx context.Context, c *conn, p protocol.ProjectInstructionsParams) (any, error) {
			return e.ProjectInstructions(ctx, p)
		}),
		protocol.MethodProjectScanInstructions: bind(func(ctx context.Context, c *conn, p protocol.ProjectIDParams) (any, error) {
			return e.ScanProjectInstructions(ctx, p)
		}),
		protocol.MethodProjectFiles: bind(func(ctx context.Context, c *conn, p protocol.ProjectFilesParams) (any, error) {
			return e.ProjectFiles(ctx, p)
		}),
		protocol.MethodProjectReadFile: bind(func(ctx context.Context, c *conn, p protocol.ProjectReadFileParams) (any, error) {
			return e.ProjectReadFile(ctx, p)
		}),
		protocol.MethodProjectReadArtifact: bind(func(ctx context.Context, c *conn, p protocol.ProjectReadArtifactParams) (any, error) {
			if !c.IsAdmin() {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "only app clients can read browser artifacts")
			}
			return e.ProjectReadArtifact(ctx, p)
		}),
		protocol.MethodProjectDiff: bind(func(ctx context.Context, c *conn, p protocol.ProjectDiffParams) (any, error) {
			return e.ProjectDiff(ctx, p)
		}),
		protocol.MethodProjectRevertTurn: bind(func(ctx context.Context, c *conn, p protocol.ProjectRevertTurnParams) (any, error) {
			return e.RevertTurn(ctx, p)
		}),
		protocol.MethodProjectKeepTask: bind(func(ctx context.Context, c *conn, p protocol.TaskWorkspaceParams) (any, error) {
			return e.KeepTaskChanges(ctx, p)
		}),
		protocol.MethodProjectDiscardTask: bind(func(ctx context.Context, c *conn, p protocol.TaskWorkspaceParams) (any, error) {
			return e.DiscardTaskWorkspace(ctx, p)
		}),

		// turns
		protocol.MethodTurnStart: bind(func(ctx context.Context, c *conn, p protocol.TurnStartParams) (any, error) {
			c.follow(p.ThreadID)
			t, err := e.StartTurn(ctx, p)
			return protocol.TurnStartResult{Turn: t}, err
		}),
		protocol.MethodTurnInterrupt: bind(func(ctx context.Context, c *conn, p protocol.TurnInterruptParams) (any, error) {
			return okResult{true}, e.InterruptTurn(p.TurnID)
		}),
		protocol.MethodPreviewList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			if !c.IsAdmin() {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "only app clients can list live previews")
			}
			return protocol.PreviewListResult{Previews: e.Previews.List()}, nil
		}),
		protocol.MethodPreviewStop: bind(func(ctx context.Context, c *conn, p protocol.PreviewStopParams) (any, error) {
			if !c.IsAdmin() {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "only app clients can stop live previews")
			}
			return okResult{true}, e.Previews.Stop(p.PreviewID)
		}),

		// approvals
		protocol.MethodApprovalList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			list, err := e.Store.ListApprovals(ctx, "pending")
			if list == nil {
				list = []protocol.Approval{}
			}
			return protocol.ApprovalListResult{Approvals: list}, err
		}),
		protocol.MethodApprovalRespond: bind(func(ctx context.Context, c *conn, p protocol.ApprovalRespondParams) (any, error) {
			if !c.IsAdmin() {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "only admin clients can answer approvals")
			}
			return e.RespondApproval(ctx, p.ApprovalID, p.Approve, p.Remember, c.id)
		}),

		// providers and models
		protocol.MethodProviderList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			ps, err := e.Providers(ctx)
			return protocol.ProviderListResult{Providers: ps}, err
		}),
		protocol.MethodModelList: bind(func(ctx context.Context, c *conn, p protocol.ModelListParams) (any, error) {
			ms, err := e.Catalog.List(ctx, p.Provider, p.IncludeHidden)
			if ms == nil {
				ms = []protocol.Model{}
			}
			return protocol.ModelListResult{Models: ms}, err
		}),
		protocol.MethodModelSetHidden: bind(func(ctx context.Context, c *conn, p protocol.ModelSetHiddenParams) (any, error) {
			return okResult{true}, e.Store.SetModelHidden(ctx, p.Provider, p.Model, p.Hidden)
		}),
		protocol.MethodModelSetPrice: bind(func(ctx context.Context, c *conn, p protocol.ModelSetPriceParams) (any, error) {
			if p.InputPerMTok < 0 || p.OutputPerMTok < 0 || p.CachedInputPerMTok < 0 {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "prices must be >= 0")
			}
			return okResult{true}, e.Store.SetModelPrice(ctx, p)
		}),
		protocol.MethodRoutingGet: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			return e.RoutingConfig(), nil
		}),
		protocol.MethodRoutingSet: bind(func(ctx context.Context, c *conn, p protocol.RoutingConfig) (any, error) {
			cfg, err := e.SetRoutingConfig(ctx, p)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			return cfg, nil
		}),
		protocol.MethodModelRoute: bind(func(ctx context.Context, c *conn, p protocol.ModelRouteParams) (any, error) {
			return e.RoutePreview(ctx, p)
		}),
		protocol.MethodModelHealth: bind(func(ctx context.Context, c *conn, p protocol.ModelHealthParams) (any, error) {
			if p.Clear != nil {
				if err := e.ClearCooldown(ctx, p.Clear.Provider, p.Clear.Model, p.Clear.CredentialID); err != nil {
					return nil, err
				}
			}
			return e.ModelHealth(ctx)
		}),
		protocol.MethodModelRefresh: bind(func(ctx context.Context, c *conn, p protocol.ModelRefreshParams) (any, error) {
			return e.RefreshModels(ctx, p.CredentialID)
		}),

		// API keys
		protocol.MethodCredentialList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			cs, err := e.Creds.List(ctx)
			if cs == nil {
				cs = []protocol.Credential{}
			}
			return protocol.CredentialListResult{Credentials: cs}, err
		}),
		protocol.MethodCredentialAdd: bind(func(ctx context.Context, c *conn, p protocol.CredentialAddParams) (any, error) {
			cred, err := e.Creds.Add(ctx, p)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			return cred, nil
		}),
		protocol.MethodCredentialTest: bind(func(ctx context.Context, c *conn, p protocol.CredentialIDParams) (any, error) {
			return e.Creds.Test(ctx, p.CredentialID)
		}),
		protocol.MethodCredentialUpdate: bind(func(ctx context.Context, c *conn, p protocol.CredentialUpdateParams) (any, error) {
			return e.Creds.Update(ctx, p)
		}),
		protocol.MethodCredentialRotate: bind(func(ctx context.Context, c *conn, p protocol.CredentialRotateParams) (any, error) {
			return e.Creds.Rotate(ctx, p.CredentialID, p.Secret)
		}),
		protocol.MethodCredentialDelete: bind(func(ctx context.Context, c *conn, p protocol.CredentialIDParams) (any, error) {
			return okResult{true}, e.Creds.Delete(ctx, p.CredentialID)
		}),

		// usage
		protocol.MethodUsageSummary: bind(func(ctx context.Context, c *conn, p protocol.UsageSummaryParams) (any, error) {
			r, err := e.UsageSummary(ctx, p)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			if r.Rows == nil {
				r.Rows = []protocol.UsageRow{}
			}
			return r, nil
		}),
		protocol.MethodUsageSetBudget: bind(func(ctx context.Context, c *conn, p protocol.UsageSetBudgetParams) (any, error) {
			return okResult{true}, e.Creds.SetBudget(ctx, p)
		}),

		// scheduled tasks
		protocol.MethodTaskList: bind(func(ctx context.Context, c *conn, p protocol.TaskListParams) (any, error) {
			list, err := e.Store.ListTasks(ctx, p.Status)
			if list == nil {
				list = []protocol.Task{}
			}
			return protocol.TaskListResult{Tasks: list}, err
		}),
		protocol.MethodTaskCreate: bind(func(ctx context.Context, c *conn, p protocol.TaskCreateParams) (any, error) {
			t, err := e.Tasks.Create(ctx, p, c.id)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			return t, nil
		}),
		protocol.MethodTaskCancel: bind(func(ctx context.Context, c *conn, p protocol.TaskIDParams) (any, error) {
			return e.Tasks.Cancel(ctx, p.TaskID)
		}),
		protocol.MethodTaskRunNow: bind(func(ctx context.Context, c *conn, p protocol.TaskIDParams) (any, error) {
			return e.Tasks.RunNow(ctx, p.TaskID)
		}),
		protocol.MethodTaskRuns: bind(func(ctx context.Context, c *conn, p protocol.TaskIDParams) (any, error) {
			runs, err := e.Store.ListTaskRuns(ctx, p.TaskID, 50)
			if runs == nil {
				runs = []protocol.TaskRun{}
			}
			return protocol.TaskRunsResult{Runs: runs}, err
		}),

		// skills
		protocol.MethodSkillList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			res := protocol.SkillListResult{Skills: []protocol.SkillInfo{}, Dirs: e.Skills.Dirs()}
			for _, sk := range e.Skills.List() {
				res.Skills = append(res.Skills, skillInfo(e.Skills, sk))
			}
			return res, nil
		}),
		protocol.MethodSkillGet: bind(func(ctx context.Context, c *conn, p protocol.SkillNameParams) (any, error) {
			sk, ok := e.Skills.Get(p.Name)
			if !ok {
				return nil, protocol.Errorf(protocol.CodeNotFound, "skill %q is not installed", p.Name)
			}
			detail, _ := json.Marshal(sk.Scripts)
			return protocol.SkillGetResult{Skill: skillInfo(e.Skills, sk), Instructions: sk.Body, ScriptsJSON: detail}, nil
		}),
		protocol.MethodSkillInstall: bind(func(ctx context.Context, c *conn, p protocol.SkillInstallParams) (any, error) {
			sk, err := e.Skills.Install(ctx, p.Source, p.Name)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			_ = e.Store.Audit(ctx, "skill.install", map[string]any{"name": sk.Name, "source": p.Source})
			return skillInfo(e.Skills, sk), nil
		}),
		protocol.MethodSkillRemove: bind(func(ctx context.Context, c *conn, p protocol.SkillNameParams) (any, error) {
			if err := e.Skills.Remove(p.Name); err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			_ = e.Store.Audit(ctx, "skill.remove", map[string]any{"name": p.Name})
			return okResult{true}, nil
		}),

		// MCP and tools
		protocol.MethodMCPList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			res := protocol.MCPListResult{Servers: []protocol.MCPServer{}}
			for _, s := range e.MCP.List() {
				res.Servers = append(res.Servers, protocol.MCPServer{Name: s.Name, Transport: s.Transport, Status: s.Status,
					Error: s.Error, ServerName: s.ServerName, ServerVersion: s.ServerVersion, Tools: s.Tools})
			}
			return res, nil
		}),
		protocol.MethodMCPRestart: bind(func(ctx context.Context, c *conn, p protocol.MCPRestartParams) (any, error) {
			if err := e.MCP.Restart(p.Name); err != nil {
				return nil, protocol.Errorf(protocol.CodeProviderError, "%v", err)
			}
			return okResult{true}, nil
		}),
		protocol.MethodToolList: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			res := protocol.ToolListResult{Tools: []protocol.ToolInfo{}}
			for _, t := range e.Tools.All() {
				src := "builtin"
				switch {
				case strings.HasPrefix(t.Name(), "mcp_"):
					src = "mcp"
				case strings.HasPrefix(t.Name(), "skill."):
					src = "skill"
				case strings.HasPrefix(t.Name(), "task."):
					src = "task"
				}
				res.Tools = append(res.Tools, protocol.ToolInfo{Name: t.Name(), Description: t.Description(), Schema: t.Schema(), Source: src})
			}
			return res, nil
		}),

		// complexity
		protocol.MethodComplexityGetDefaults: bind(func(ctx context.Context, c *conn, _ empty) (any, error) {
			return e.ComplexityDefaults(), nil
		}),
		protocol.MethodComplexitySetDefaults: bind(func(ctx context.Context, c *conn, p protocol.ComplexityDefaults) (any, error) {
			d, err := e.SetComplexityDefaults(ctx, p)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidParams, "%v", err)
			}
			return d, nil
		}),
	}
}

func major(v string) string {
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}

func skillInfo(reg *skills.Registry, sk *skills.Skill) protocol.SkillInfo {
	info := protocol.SkillInfo{Name: sk.Name, Description: sk.Description, Version: sk.Version, Runtime: sk.Runtime.Type,
		RiskLevel: sk.RiskLevel, Dir: sk.Dir, Error: sk.Error, Scripts: []string{}}
	for n := range sk.Scripts {
		info.Scripts = append(info.Scripts, n)
	}
	sort.Strings(info.Scripts)
	root, _ := filepath.Abs(skills.InstallDir(reg.Config()))
	dir, _ := filepath.Abs(sk.Dir)
	info.Removable = filepath.Dir(dir) == root
	return info
}
