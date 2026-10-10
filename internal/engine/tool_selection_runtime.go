package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/mcp"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/toolselect"
	"reflect"
	"sort"
	"strings"
	"time"
)

type selectionContextKey struct{}
type turnSelection struct {
	fatal                       bool
	actual                      []turnTool
	state                       *toolselect.State
	baseline                    []llm.ToolSpec
	project                     *protocol.Project
	threadID, request, fallback string
	durationMS                  int64
}

func selectionFrom(ctx context.Context) *turnSelection {
	s, _ := ctx.Value(selectionContextKey{}).(*turnSelection)
	return s
}
func (e *Engine) startToolSelection(ctx context.Context, th protocol.Thread, snapshot pluginSnapshot, project *protocol.Project, request string) (s *turnSelection) {
	s = &turnSelection{project: project, threadID: th.ID, request: request}
	started := time.Now()
	defer func() {
		s.durationMS = time.Since(started).Milliseconds()
		if recover() != nil {
			if s.state != nil {
				s.state.Fallback("selection_error")
			} else {
				s.fallback = "selection_error"
				s.fatal = true
			}
		}
	}()
	actual, catalog, err := e.permittedTurnCatalog(ctx, snapshot, project)
	reserved := false
	for _, c := range actual {
		if c.tool.Name() == "tools.discover" || c.spec.Name == tools.ToWire("tools.discover") {
			reserved = true
			continue
		}
		s.actual = append(s.actual, c)
		s.baseline = append(s.baseline, c.spec)
	}
	if err != nil || reserved {
		s.fallback = "catalog_error"
		return s
	}
	discovery := &discoveryTool{}
	spec := llm.ToolSpec{Name: tools.ToWire(discovery.Name()), Description: discovery.Description(), Schema: discovery.Schema()}
	catalog.Entries = append(catalog.Entries, toolselect.Entry{CanonicalName: discovery.Name(), WireName: spec.Name, Family: "core", Origin: "engine", Spec: spec})
	s.state, _, err = toolselect.Start(catalog, e.toolSignals(ctx, s))
	if err != nil {
		s.fallback = "catalog_error"
		return s
	}
	discovery.state = s.state
	discovery.before = func(ctx context.Context) error {
		if e.discoveryHook != nil {
			e.discoveryHook()
		}
		return e.refreshSelectionPermissions(ctx, s)
	}
	s.actual = append(s.actual, turnTool{tool: discovery, spec: spec})
	if e.selectionHook != nil {
		e.selectionHook()
	}
	return s
}
func (e *Engine) toolSignals(ctx context.Context, s *turnSelection) toolselect.Signals {
	signals := toolselect.Signals{Request: s.request, RequiredFamilies: e.requiredToolFamilies(s.threadID)}
	if e.Work == nil || e.Store == nil {
		return signals
	}
	w, open, err := e.Store.OpenWorkForThread(ctx, s.threadID)
	if err != nil {
		if s.state != nil {
			s.state.Fallback("selection_error")
		}
		return signals
	}
	if !open {
		return signals
	}
	d, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		if s.state != nil {
			s.state.Fallback("selection_error")
		}
		return signals
	}
	signals.Depth = d.Work.WorkflowDepth
	var active []string
	for _, n := range d.Nodes {
		if n.ValidUntil != nil || n.SupersededBy != "" {
			continue
		}
		if n.Kind == protocol.NodeTask && (n.Status == protocol.StatusInProgress || n.Status == "ready") {
			active = append(active, n.ID)
		}
		if n.Kind == protocol.NodeCriterion && n.Status != protocol.StatusCompleted && n.Status != protocol.StatusSuperseded {
			var meta struct {
				CheckType string `json:"check_type"`
			}
			if json.Unmarshal(n.Content, &meta) == nil && (meta.CheckType == "browser" || meta.CheckType == "visual") {
				signals.PlannedCheckFamilies = append(signals.PlannedCheckFamilies, "browser")
			}
		}
	}
	sort.Strings(active)
	signals.TaskID = strings.Join(active, "+")
	return signals
}
func (e *Engine) currentSelectionProject(ctx context.Context, s *turnSelection) (*protocol.Project, error) {
	if s.project == nil || e.Projects == nil {
		return s.project, nil
	}
	p, err := e.Projects.Get(ctx, s.project.ID)
	if err != nil {
		return nil, errors.New("tool policy unavailable")
	}
	enabled := e.ComputerUseDefault(ctx).Enabled
	if p.Tools.ComputerUse != nil {
		enabled = *p.Tools.ComputerUse
	}
	p.Tools.ComputerUse = &enabled
	return &p, nil
}
func (e *Engine) refreshSelectionPermissions(ctx context.Context, s *turnSelection) error {
	p, err := e.currentSelectionProject(ctx, s)
	if err != nil {
		if s.state != nil {
			s.state.Restrict(map[string]bool{})
		} else {
			s.baseline = nil
		}
		return err
	}
	allowed := map[string]bool{}
	for _, c := range s.actual {
		allowed[c.tool.Name()] = e.permittedToolFor(ctx, c, p)
	}
	if s.state != nil {
		s.state.Restrict(allowed)
	} else {
		var kept []llm.ToolSpec
		for _, spec := range s.baseline {
			for _, c := range s.actual {
				if c.spec.Name == spec.Name && allowed[c.tool.Name()] {
					kept = append(kept, spec)
					break
				}
			}
		}
		s.baseline = kept
	}
	return nil
}
func (e *Engine) refreshToolSelection(ctx context.Context, s *turnSelection) {
	started := time.Now()
	defer func() {
		s.durationMS += time.Since(started).Milliseconds()
		if recover() != nil {
			if s.state != nil {
				s.state.Fallback("selection_error")
			} else {
				s.fallback = "selection_error"
			}
		}
	}()
	if err := e.refreshSelectionPermissions(ctx, s); err != nil {
		if s.state != nil {
			s.state.Fallback("selection_error")
		}
		return
	}
	if s.state != nil {
		s.state.Advance(e.toolSignals(ctx, s))
	}
	if e.selectionHook != nil {
		e.selectionHook()
	}
}
func (s *turnSelection) specs() []llm.ToolSpec {
	if s.state != nil {
		return s.state.Specs()
	}
	return append([]llm.ToolSpec(nil), s.baseline...)
}
func resolveTurnTool(catalog []turnTool, name string) (turnTool, bool) {
	var found turnTool
	count := 0
	for _, c := range catalog {
		if c.tool.Name() == name || c.spec.Name == name {
			found = c
			count++
		}
	}
	return found, count == 1
}

func sameTool(a, b tools.Tool) bool {
	if a == nil || b == nil {
		return false
	}
	if left, ok := a.(tools.SelectionIdentity); ok {
		right, ok := b.(tools.SelectionIdentity)
		if !ok {
			return false
		}
		x, y := left.SelectionIdentity(), right.SelectionIdentity()
		return reflect.TypeOf(x) != nil && reflect.TypeOf(x) == reflect.TypeOf(y) && reflect.TypeOf(x).Comparable() && x == y
	}
	return reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.TypeOf(a).Comparable() && a == b
}
func (e *Engine) selectionToolAvailable(c turnTool) bool {
	if c.plugin {
		return true
	}
	if _, ok := c.tool.(*discoveryTool); ok {
		return true
	}
	if e.Tools == nil {
		return false
	}
	current, ok := e.Tools.Get(c.tool.Name())
	return ok && sameTool(c.tool, current)
}
func (e *Engine) selectionFailure(ctx context.Context, turn protocol.Turn, call llm.ToolCall, name, reason string) toolRunResult {
	it, err := e.newItem(ctx, turn, protocol.ItemToolCall)
	if err == nil {
		it.Tool = &protocol.ToolCallData{CallID: call.ID, Name: name, Args: call.Args, Error: reason}
		it.Status = protocol.ItemDenied
		_ = e.saveAndPublish(ctx, it, protocol.NotifyItemCompleted)
	}
	return toolRunResult{Output: reason, ModelOutput: reason, IsError: true}
}
func (e *Engine) runDiscovery(ctx, sctx context.Context, turn protocol.Turn, call llm.ToolCall, tool *discoveryTool) toolRunResult {
	if tool == nil {
		return e.selectionFailure(sctx, turn, call, "tools.discover", "discovery unavailable")
	}
	it, err := e.newItem(sctx, turn, protocol.ItemToolCall)
	if err != nil {
		return toolRunResult{Output: "discovery unavailable", ModelOutput: "discovery unavailable", IsError: true}
	}
	it.Tool = &protocol.ToolCallData{CallID: call.ID, Name: tool.Name(), Args: call.Args, Risk: string(tools.RiskGreen)}
	_ = e.saveAndPublish(sctx, it, protocol.NotifyItemStarted)
	output, err := tool.Call(ctx, call.Args)
	it.Status = protocol.ItemCompleted
	if err != nil {
		it.Status = protocol.ItemFailed
		it.Tool.Error = err.Error()
		output = err.Error()
	} else {
		it.Tool.Output = output
	}
	_ = e.saveAndPublish(sctx, it, protocol.NotifyItemCompleted)
	return toolRunResult{Output: output, ModelOutput: output, IsError: err != nil}
}

func (e *Engine) permittedTool(c turnTool, p *protocol.Project) bool {
	return e.permittedToolFor(context.Background(), c, p)
}
func (e *Engine) permittedToolFor(ctx context.Context, c turnTool, p *protocol.Project) bool {
	if !catalogToolAllowed(c, p) {
		return false
	}
	if c.tool.Name() == "work.update" && (e.Cfg == nil || !e.optimizationPolicy(ctx).DesignedWorkflow) {
		return false
	}
	if !c.plugin {
		if metadata, ok := c.tool.(tools.SelectionMetadata); ok {
			family, _ := metadata.SelectionMetadata()
			if strings.HasPrefix(family, "mcp:") {
				server := strings.TrimPrefix(family, "mcp:")
				prefix := "mcp_" + server + "_"
				if !strings.HasPrefix(c.tool.Name(), prefix) || e.Cfg == nil {
					return false
				}
				for _, cfg := range e.Cfg.MCPServers {
					if cfg.Name == server {
						return mcp.ToolAllowed(cfg, strings.TrimPrefix(c.tool.Name(), prefix))
					}
				}
				return false
			}
		}
	}
	return true
}

// Keep the frozen workspace/recorder but narrow capabilities using current
// project policy. Existing tool-specific sandbox and approval rules still apply.
func currentSelectionScope(ctx context.Context, p *protocol.Project) context.Context {
	scope := tools.ScopeFrom(ctx)
	if scope == nil || p == nil {
		return ctx
	}
	narrowed := *scope
	narrowed.AllowNet = scope.AllowNet && boolOr(p.Tools.Network, false)
	narrowed.UseCompute = scope.UseCompute && boolOr(p.Tools.Compute, false)
	narrowed.AllowComputerUse = scope.AllowComputerUse && boolOr(p.Tools.ComputerUse, false)
	narrowed.ComputeVCPUs = intOr(p.Tools.ComputeVCPUs, 0)
	narrowed.ComputeMemoryMiB = intOr(p.Tools.ComputeMemoryMiB, 0)
	narrowed.ComputeDiskMiB = intOr(p.Tools.ComputeDiskMiB, 0)
	return tools.WithScope(ctx, &narrowed)
}
