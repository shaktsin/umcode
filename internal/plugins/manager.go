package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/hooks"
	"github.com/shaktsin/umcode/internal/mcp"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/skills"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type Options struct {
	Store   *store.Store
	Secrets secrets.Store
	Log     *slog.Logger
}

type projectSnapshotState struct {
	snapshot *Snapshot
	dirty    bool
}

type preparedSnapshot struct {
	projectID string
	snapshot  *Snapshot
}

type Manager struct {
	st           *store.Store
	secrets      secrets.Store
	log          *slog.Logger
	activationMu sync.Mutex

	mu             sync.Mutex
	projects       map[string]*projectSnapshotState
	rootRefs       map[string]int
	pendingCleanup map[string][]func()
	closed         bool
}

func NewManager(options Options) (*Manager, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("plugin manager requires a store")
	}
	if options.Log == nil {
		options.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{st: options.Store, secrets: options.Secrets, log: options.Log, projects: map[string]*projectSnapshotState{}, rootRefs: map[string]int{}, pendingCleanup: map[string][]func(){}}, nil
}

func (m *Manager) Acquire(ctx context.Context, projectID string) (*Snapshot, error) {
	// Serialize reads against installer activation transactions so no turn can
	// publish a partial cross-project generation while an update is staged.
	m.activationMu.Lock()
	defer m.activationMu.Unlock()
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
	if err := m.persistComponentHealth(ctx, projectID, candidate); err != nil {
		candidate.Release()
		return nil, err
	}

	if err := m.publish(projectID, candidate); err != nil {
		return nil, err
	}
	candidate.addRef()
	return candidate, nil
}

// Activate builds and publishes a candidate snapshot, returning component
// failures to the installer so persistence can be rolled back atomically.
func (m *Manager) Activate(ctx context.Context, projectID string) error {
	return m.ActivateMany(ctx, []string{projectID})
}

// ActivateMany prepares every project's capability generation before any one
// becomes visible, then publishes them together under the manager lock.
func (m *Manager) ActivateMany(ctx context.Context, projectIDs []string) error {
	m.activationMu.Lock()
	defer m.activationMu.Unlock()
	return m.activateManyLocked(ctx, projectIDs)
}

// BeginLifecycleMutation prevents turn acquisition from observing persistence
// while an installer is changing a global plugin record. The returned
// activation callback must be used to publish the resulting project set.
func (m *Manager) BeginLifecycleMutation() (func(context.Context, []string) error, func()) {
	m.activationMu.Lock()
	return m.activateManyLocked, m.activationMu.Unlock
}

func (m *Manager) activateManyLocked(ctx context.Context, projectIDs []string) error {
	preparedSnapshots := make([]preparedSnapshot, 0, len(projectIDs))
	seen := map[string]bool{}
	for _, projectID := range projectIDs {
		if seen[projectID] {
			continue
		}
		seen[projectID] = true
		inputs, generation, err := m.loadInputs(ctx, projectID)
		if err != nil {
			releasePrepared(preparedSnapshots)
			return err
		}
		candidate, err := m.buildSnapshot(inputs, generation)
		if err != nil {
			releasePrepared(preparedSnapshots)
			return err
		}
		preparedSnapshots = append(preparedSnapshots, preparedSnapshot{projectID, candidate})
	}
	if err := m.persistComponentHealthBatch(ctx, preparedSnapshots); err != nil {
		releasePrepared(preparedSnapshots)
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		releasePrepared(preparedSnapshots)
		return fmt.Errorf("plugin manager is closed")
	}
	var previous []*Snapshot
	for _, item := range preparedSnapshots {
		if current := m.projects[item.projectID]; current != nil {
			previous = append(previous, current.snapshot)
		}
		m.projects[item.projectID] = &projectSnapshotState{snapshot: item.snapshot}
	}
	m.mu.Unlock()
	for _, snapshot := range previous {
		snapshot.Release()
	}
	return nil
}

func releasePrepared(preparedSnapshots []preparedSnapshot) {
	for _, item := range preparedSnapshots {
		item.snapshot.Release()
	}
}

func (m *Manager) persistComponentHealth(ctx context.Context, projectID string, snapshot *Snapshot) error {
	return m.persistComponentHealthBatch(ctx, []preparedSnapshot{{projectID, snapshot}})
}

func (m *Manager) persistComponentHealthBatch(ctx context.Context, snapshots []preparedSnapshot) error {
	var sets []store.PluginComponentHealthSet
	for _, item := range snapshots {
		projectPlugins, err := m.st.ListProjectPlugins(ctx, item.projectID)
		if err != nil {
			return err
		}
		for _, projectPlugin := range projectPlugins {
			states := item.snapshot.componentHealth[projectPlugin.PluginID]
			values := make([]store.PluginComponentHealth, 0, len(states))
			for component, health := range states {
				values = append(values, store.PluginComponentHealth{PluginID: projectPlugin.PluginID, ProjectID: item.projectID, Component: component, Status: health.Status, Error: health.Error})
			}
			sort.Slice(values, func(i, j int) bool { return values[i].Component < values[j].Component })
			sets = append(sets, store.PluginComponentHealthSet{PluginID: projectPlugin.PluginID, ProjectID: item.projectID, Values: values})
		}
	}
	return m.st.ReplacePluginComponentHealthBatch(ctx, sets)
}

func (m *Manager) publish(projectID string, candidate *Snapshot) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		candidate.Release()
		return fmt.Errorf("plugin manager is closed")
	}
	previous := m.projects[projectID]
	m.projects[projectID] = &projectSnapshotState{snapshot: candidate}
	m.mu.Unlock()
	if previous != nil && previous.snapshot != candidate {
		previous.snapshot.Release()
	}
	return nil
}

func (m *Manager) Invalidate(projectID string) {
	m.mu.Lock()
	if state := m.projects[projectID]; state != nil {
		state.dirty = true
	}
	m.mu.Unlock()
}

func (m *Manager) ComponentHealth(projectID, pluginID string) map[string]ComponentHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.projects[projectID]
	if state == nil || state.snapshot == nil {
		return nil
	}
	source := state.snapshot.componentHealth[pluginID]
	result := make(map[string]ComponentHealth, len(source))
	for component, health := range source {
		result[component] = health
	}
	return result
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

// DeferCleanup runs cleanup after every snapshot referencing root has retired.
func (m *Manager) DeferCleanup(root string, cleanup func()) {
	m.mu.Lock()
	if m.rootRefs[root] > 0 {
		m.pendingCleanup[root] = append(m.pendingCleanup[root], cleanup)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	cleanup()
}

func (m *Manager) trackSnapshot(snapshot *Snapshot, installations []store.PluginInstallation) {
	roots := make([]string, 0, len(installations))
	seen := map[string]bool{}
	m.mu.Lock()
	for _, installation := range installations {
		if !seen[installation.Root] {
			seen[installation.Root] = true
			roots = append(roots, installation.Root)
			m.rootRefs[installation.Root]++
		}
	}
	m.mu.Unlock()
	snapshot.onClose = func() {
		var cleanups []func()
		m.mu.Lock()
		for _, root := range roots {
			m.rootRefs[root]--
			if m.rootRefs[root] <= 0 {
				delete(m.rootRefs, root)
				cleanups = append(cleanups, m.pendingCleanup[root]...)
				delete(m.pendingCleanup, root)
			}
		}
		m.mu.Unlock()
		for _, cleanup := range cleanups {
			cleanup()
		}
	}
}

type snapshotInput struct {
	installation store.PluginInstallation
	project      store.ProjectPlugin
	pkg          Package
	runtime      runtimeValues
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
		runtime, err := m.loadRuntimeValues(installation.ID, pkg, projectPlugin.Settings)
		if err != nil {
			return nil, "", err
		}
		inputs = append(inputs, snapshotInput{installation: installation, project: projectPlugin, pkg: pkg, runtime: runtime})
		settings, _ := json.Marshal(projectPlugin.Settings)
		generationParts = append(generationParts, installation.ID+"\x00"+installation.Digest+"\x00"+string(settings)+"\x00"+runtime.digest)
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
	componentHealth := map[string]map[string]ComponentHealth{}
	type mcpOwner struct {
		pluginID, pluginName, component string
		secretValues                    []string
	}
	mcpOwners := map[string]mcpOwner{}
	for _, input := range inputs {
		installations = append(installations, input.installation)
		componentHealth[input.installation.ID] = map[string]ComponentHealth{}
		var paths []string
		for _, component := range input.pkg.Skills {
			path, err := containedInstalledPath(input.pkg.Root, component.Path)
			if err != nil {
				return nil, err
			}
			paths = append(paths, path)
			componentHealth[input.installation.ID]["skill:"+component.Name] = ComponentHealth{Status: "available"}
		}
		skillRoots = append(skillRoots, skills.Root{PluginID: input.pkg.Name, Paths: paths, Config: input.project.Settings, RuntimeValues: input.runtime.replacements, SecretValues: input.runtime.secretValues})
		for _, component := range input.pkg.MCPServers {
			cfg, err := resolvePluginMCPConfig(component.Config, input.runtime)
			if err != nil {
				return nil, fmt.Errorf("plugin %s MCP %s: %w", input.installation.ID, component.Name, err)
			}
			cfg.Name = input.pkg.Name + "__" + component.Name
			mcpOwners[cfg.Name] = mcpOwner{pluginID: input.installation.ID, pluginName: input.pkg.Name, component: "mcp:" + component.Name, secretValues: append([]string(nil), input.runtime.secretValues...)}
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
			declaration.PluginVersion = input.installation.Version
			declaration.Root = input.pkg.Root
			resolvedEnv, err := resolveRuntimeMap(declaration.Env, input.runtime, false)
			if err != nil {
				return nil, fmt.Errorf("plugin %s hook %s: %w", input.installation.ID, declaration.Event, err)
			}
			declaration.Env = resolvedEnv
			declaration.SecretValues = append([]string(nil), input.runtime.secretValues...)
			declarations = append(declarations, declaration)
			componentHealth[input.installation.ID]["hook:"+string(declaration.Event)] = ComponentHealth{Status: "available"}
		}
	}
	skillSnapshot, skillErrors := skills.CompileSnapshot(skillRoots)
	if len(skillErrors) > 0 {
		return nil, skillErrors[0]
	}
	var mcpManager *mcp.Manager
	if len(mcpConfigs) > 0 {
		// MCP startup errors can contain credential-bearing URLs or echoed
		// headers. They are redacted before entering component health; suppress
		// the MCP manager's raw diagnostic log for plugin-owned servers.
		mcpManager = mcp.NewManager(mcpConfigs, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := mcpManager.Start(true); err != nil {
			mcpManager.Close()
			var secretValues []string
			for _, input := range inputs {
				secretValues = append(secretValues, input.runtime.secretValues...)
			}
			return nil, fmt.Errorf("%s", redactSecrets(err.Error(), secretValues))
		}
		for _, server := range mcpManager.List() {
			owner, ok := mcpOwners[server.Name]
			if !ok {
				continue
			}
			status := "available"
			if server.Status != mcp.StatusReady {
				status = server.Status
			}
			componentHealth[owner.pluginID][owner.component] = ComponentHealth{Status: status, Error: redactSecrets(server.Error, owner.secretValues)}
		}
	}
	var snapshotTools []tools.Tool
	if mcpManager != nil {
		snapshotTools = mcpManager.Tools()
	}
	byTool := make(map[string]tools.Tool, len(snapshotTools))
	for index, tool := range snapshotTools {
		// The registered server identity is unambiguous even when server names
		// share prefixes or contain underscores.
		wrapped := &redactingTool{Tool: tool}
		if metadata, ok := tool.(tools.SelectionMetadata); ok {
			family, _ := metadata.SelectionMetadata()
			serverName := strings.TrimPrefix(family, "mcp:")
			if owner, ok := mcpOwners[serverName]; ok {
				wrapped.values = append([]string(nil), owner.secretValues...)
				wrapped.family = "plugin:" + owner.pluginName
				wrapped.origin = "plugin:" + owner.pluginID + "/mcp:" + serverName
			}
		}

		snapshotTools[index] = wrapped
		byTool[wrapped.Name()] = wrapped
	}
	snapshot := &Snapshot{generation: generation, tools: snapshotTools, byTool: byTool, skills: skillSnapshot, hooks: hooks.Set{Declarations: declarations}, mcp: mcpManager, installations: installations, componentHealth: componentHealth, refs: 1}
	m.trackSnapshot(snapshot, installations)
	return snapshot, nil
}

type runtimeValues struct {
	replacements map[string]string
	secretValues []string
	digest       string
}

func (m *Manager) loadRuntimeValues(pluginID string, pkg Package, settings map[string]any) (runtimeValues, error) {
	result := runtimeValues{replacements: map[string]string{}}
	var digestParts []string
	for _, setting := range pkg.Settings {
		if setting.Secret {
			var value string
			var err error
			if m.secrets != nil {
				value, err = m.secrets.Get("plugin/" + pluginID + "/" + setting.Name)
			} else {
				err = secrets.ErrNotFound
			}
			if err != nil {
				if setting.Required {
					return runtimeValues{}, fmt.Errorf("plugin %s requires secret %s", pluginID, setting.Name)
				}
				continue
			}
			result.replacements["{{secret:"+setting.Name+"}}"] = value
			result.secretValues = append(result.secretValues, value)
			sum := sha256.Sum256([]byte(value))
			digestParts = append(digestParts, setting.Name+":"+hex.EncodeToString(sum[:]))
			continue
		}
		if value, ok := settings[setting.Name]; ok {
			text := fmt.Sprint(value)
			result.replacements["{{setting:"+setting.Name+"}}"] = text
		}
	}
	sort.Strings(digestParts)
	digest := sha256.Sum256([]byte(strings.Join(digestParts, "\x00")))
	result.digest = hex.EncodeToString(digest[:])
	return result, nil
}

func resolveRuntimeString(value string, runtime runtimeValues) (string, error) {
	for token, replacement := range runtime.replacements {
		value = strings.ReplaceAll(value, token, replacement)
	}
	if strings.Contains(value, "{{secret:") || strings.Contains(value, "{{setting:") {
		return "", fmt.Errorf("runtime value references an undeclared or unset plugin setting")
	}
	return value, nil
}

func resolveRuntimeMap(values map[string]string, runtime runtimeValues, rejectHostExpansion bool) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, value := range values {
		if rejectHostExpansion && strings.Contains(value, "$") {
			return nil, fmt.Errorf("host environment expansion is not allowed")
		}
		resolved, err := resolveRuntimeString(value, runtime)
		if err != nil {
			return nil, err
		}
		result[key] = resolved
	}
	return result, nil
}

func resolvePluginMCPConfig(cfg config.MCPServerConfig, runtime runtimeValues) (config.MCPServerConfig, error) {
	if len(cfg.EnvVars) > 0 || len(cfg.EnvHTTPHeaders) > 0 || cfg.BearerTokenEnvVar != "" {
		return config.MCPServerConfig{}, fmt.Errorf("forwarding the host environment is not allowed for plugin MCP servers")
	}
	resolvedEnv, err := resolveRuntimeMap(cfg.Env, runtime, true)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	resolvedHeaders := make(map[string]string, len(cfg.HTTPHeaders))
	for key, value := range cfg.HTTPHeaders {
		lower := strings.ToLower(strings.TrimSpace(key))
		if (lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "x-api-key") && !strings.Contains(value, "{{secret:") {
			return config.MCPServerConfig{}, fmt.Errorf("sensitive HTTP header %s must use a declared secret reference", key)
		}
		resolved, err := resolveRuntimeString(value, runtime)
		if err != nil {
			return config.MCPServerConfig{}, err
		}
		resolvedHeaders[key] = resolved
	}
	resolvedURL, err := resolveRuntimeString(cfg.URL, runtime)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	resolvedCommand, err := resolveRuntimeString(cfg.Command, runtime)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	resolvedArgs := make([]string, len(cfg.Args))
	for index, arg := range cfg.Args {
		resolvedArgs[index], err = resolveRuntimeString(arg, runtime)
		if err != nil {
			return config.MCPServerConfig{}, err
		}
	}
	cfg.Env = resolvedEnv
	cfg.EnvVars = nil
	cfg.HTTPHeaders = resolvedHeaders
	cfg.EnvHTTPHeaders = nil
	cfg.BearerTokenEnvVar = ""
	cfg.URL = resolvedURL
	cfg.Command = resolvedCommand
	cfg.Args = resolvedArgs
	cfg.DisableHostEnvExpansion = true
	return cfg, nil
}

func containedInstalledPath(root, relative string) (string, error) {
	_, absolute, err := resolveResource(root, relative)
	if err != nil {
		return "", err
	}
	return absolute, nil
}

func redactSecrets(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
			if quoted, err := json.Marshal(secret); err == nil && len(quoted) >= 2 {
				value = strings.ReplaceAll(value, string(quoted[1:len(quoted)-1]), "[REDACTED]")
			}
			value = strings.ReplaceAll(value, url.QueryEscape(secret), "[REDACTED]")
			value = strings.ReplaceAll(value, url.PathEscape(secret), "[REDACTED]")
		}
	}
	return value
}

type redactingTool struct {
	family, origin string
	tools.Tool
	values []string
}

func (t *redactingTool) Name() string        { return redactSecrets(t.Tool.Name(), t.values) }
func (t *redactingTool) Description() string { return redactSecrets(t.Tool.Description(), t.values) }
func (t *redactingTool) Schema() json.RawMessage {
	return json.RawMessage(redactSecrets(string(t.Tool.Schema()), t.values))
}
func (t *redactingTool) Assess(args json.RawMessage) (tools.Risk, string) {
	risk, summary := t.Tool.Assess(args)
	return risk, redactSecrets(summary, t.values)
}
func (t *redactingTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	result, err := t.Tool.Call(ctx, args)
	result = redactSecrets(result, t.values)
	if err != nil {
		err = fmt.Errorf("%s", redactSecrets(err.Error(), t.values))
	}
	return result, err
}

func (t *redactingTool) SelectionMetadata() (string, string) { return t.family, t.origin }
