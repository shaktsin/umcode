package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/mcp"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type Options struct {
	Store *store.Store
	Log   *slog.Logger
}

type projectSnapshotState struct {
	snapshot *Snapshot
	dirty    bool
}

type Manager struct {
	st  *store.Store
	log *slog.Logger

	mu       sync.Mutex
	projects map[string]*projectSnapshotState
	closed   bool
}

func NewManager(options Options) (*Manager, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("plugin manager requires a store")
	}
	if options.Log == nil {
		options.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{st: options.Store, log: options.Log, projects: map[string]*projectSnapshotState{}}, nil
}

func (m *Manager) Acquire(ctx context.Context, projectID string) (*Snapshot, error) {
	inputs, generation, err := m.loadInputs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin manager is closed")
	}
	state := m.projects[projectID]
	if state != nil && !state.dirty && state.snapshot.generation == generation && state.snapshot.addRef() {
		snapshot := state.snapshot
		m.mu.Unlock()
		return snapshot, nil
	}
	var fallback *Snapshot
	if state != nil {
		fallback = state.snapshot
	}
	m.mu.Unlock()

	candidate, buildErr := m.buildSnapshot(inputs, generation)
	if buildErr != nil {
		if fallback != nil {
			m.restoreInstallations(ctx, fallback.installations)
			m.mu.Lock()
			if current := m.projects[projectID]; current != nil && current.snapshot == fallback {
				current.dirty = false
			}
			ok := fallback.addRef()
			m.mu.Unlock()
			if ok {
				m.log.Warn("plugin activation failed; preserving prior snapshot", "project", projectID, "err", buildErr)
				return fallback, nil
			}
		}
		return nil, buildErr
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		candidate.Release()
		return nil, fmt.Errorf("plugin manager is closed")
	}
	previous := m.projects[projectID]
	m.projects[projectID] = &projectSnapshotState{snapshot: candidate}
	candidate.addRef()
	m.mu.Unlock()
	if previous != nil && previous.snapshot != candidate {
		previous.snapshot.Release()
	}
	return candidate, nil
}

func (m *Manager) Invalidate(projectID string) {
	m.mu.Lock()
	if state := m.projects[projectID]; state != nil {
		state.dirty = true
	}
	m.mu.Unlock()
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	var snapshots []*Snapshot
	for _, state := range m.projects {
		snapshots = append(snapshots, state.snapshot)
	}
	m.projects = map[string]*projectSnapshotState{}
	m.mu.Unlock()
	for _, snapshot := range snapshots {
		snapshot.Release()
	}
}

type snapshotInput struct {
	installation store.PluginInstallation
	project      store.ProjectPlugin
	pkg          Package
}

func (m *Manager) loadInputs(ctx context.Context, projectID string) ([]snapshotInput, string, error) {
	projectPlugins, err := m.st.ListProjectPlugins(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	var inputs []snapshotInput
	var generationParts []string
	for _, projectPlugin := range projectPlugins {
		if !projectPlugin.Enabled {
			continue
		}
		installation, err := m.st.GetPluginInstallation(ctx, projectPlugin.PluginID)
		if err != nil {
			return nil, "", err
		}
		pkg, err := LoadPackage(installation.Root)
		if err != nil {
			return nil, "", fmt.Errorf("load installed plugin %s: %w", installation.ID, err)
		}
		inputs = append(inputs, snapshotInput{installation: installation, project: projectPlugin, pkg: pkg})
		settings, _ := json.Marshal(projectPlugin.Settings)
		generationParts = append(generationParts, installation.ID+"\x00"+installation.Digest+"\x00"+string(settings))
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].installation.ID < inputs[j].installation.ID })
	sort.Strings(generationParts)
	digest := sha256.Sum256([]byte(strings.Join(generationParts, "\x00")))
	return inputs, hex.EncodeToString(digest[:]), nil
}

func (m *Manager) buildSnapshot(inputs []snapshotInput, generation string) (*Snapshot, error) {
	var skillRoots []skills.Root
	var mcpConfigs []config.MCPServerConfig
	var declarations []hooks.Declaration
	installations := make([]store.PluginInstallation, 0, len(inputs))
	for _, input := range inputs {
		installations = append(installations, input.installation)
		var paths []string
		for _, component := range input.pkg.Skills {
			path, err := containedInstalledPath(input.pkg.Root, component.Path)
			if err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
		skillRoots = append(skillRoots, skills.Root{PluginID: input.pkg.Name, Paths: paths, Config: input.project.Settings})
		for _, component := range input.pkg.MCPServers {
			cfg := component.Config
			cfg.Name = input.pkg.Name + "__" + component.Name
			if cfg.Cwd == "" {
				cfg.Cwd = input.pkg.Root
			} else if !filepath.IsAbs(cfg.Cwd) {
				cwd, err := containedInstalledPath(input.pkg.Root, cfg.Cwd)
				if err != nil {
					return nil, err
				}
				cfg.Cwd = cwd
			}
			mcpConfigs = append(mcpConfigs, cfg)
		}
		for _, declaration := range input.pkg.Hooks {
			declaration.PluginID = input.installation.ID
			declaration.Root = input.pkg.Root
			declarations = append(declarations, declaration)
		}
	}
	skillSnapshot, skillErrors := skills.CompileSnapshot(skillRoots)
	if len(skillErrors) > 0 {
		return nil, skillErrors[0]
	}
	var mcpManager *mcp.Manager
	if len(mcpConfigs) > 0 {
		mcpManager = mcp.NewManager(mcpConfigs, m.log)
		if err := mcpManager.Start(true); err != nil {
			mcpManager.Close()
			return nil, err
		}
	}
	var snapshotTools []tools.Tool
	if mcpManager != nil {
		snapshotTools = mcpManager.Tools()
	}
	byTool := make(map[string]tools.Tool, len(snapshotTools))
	for _, tool := range snapshotTools {
		byTool[tool.Name()] = tool
	}
	return &Snapshot{generation: generation, tools: snapshotTools, byTool: byTool, skills: skillSnapshot, hooks: hooks.Set{Declarations: declarations}, mcp: mcpManager, installations: installations, refs: 1}, nil
}

func containedInstalledPath(root, relative string) (string, error) {
	_, absolute, err := resolveResource(root, relative)
	if err != nil {
		return "", err
	}
	return absolute, nil
}

func (m *Manager) restoreInstallations(ctx context.Context, installations []store.PluginInstallation) {
	for _, installation := range installations {
		if err := m.st.UpsertPluginInstallation(ctx, installation); err != nil {
			m.log.Error("failed to restore plugin installation after activation failure", "plugin", installation.ID, "err", err)
		}
	}
}
