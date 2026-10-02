package protocol

import "time"

const (
	MethodPluginInspect    = "plugin/inspect"
	MethodPluginInstall    = "plugin/install"
	MethodPluginList       = "plugin/list"
	MethodPluginGet        = "plugin/get"
	MethodPluginSetEnabled = "plugin/setEnabled"
	MethodPluginConfigure  = "plugin/configure"
	MethodPluginReload     = "plugin/reload"
	MethodPluginUninstall  = "plugin/uninstall"
)

type PluginDiagnostic struct {
	Code        string `json:"code"`
	Phase       string `json:"phase"`
	Severity    string `json:"severity"`
	Component   string `json:"component,omitempty"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type PluginComponentInfo struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Required  bool   `json:"required"`
	Supported bool   `json:"supported"`
	Health    string `json:"health"`
	Error     string `json:"error,omitempty"`
}

type PluginExecutableInfo struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Required bool     `json:"required"`
}

type PluginHookFailureInfo struct {
	PluginVersion string    `json:"pluginVersion,omitempty"`
	Event         string    `json:"event"`
	Error         string    `json:"error"`
	CreatedAt     time.Time `json:"createdAt"`
}

type PluginSettingInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
}

type PluginInfo struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Version      string                  `json:"version,omitempty"`
	Format       string                  `json:"format"`
	Source       string                  `json:"source"`
	Mode         string                  `json:"mode"`
	Enabled      bool                    `json:"enabled"`
	Settings     map[string]any          `json:"settings"`
	Schema       []PluginSettingInfo     `json:"schema"`
	Components   []PluginComponentInfo   `json:"components"`
	Diagnostics  []PluginDiagnostic      `json:"diagnostics"`
	Executables  []PluginExecutableInfo  `json:"executables"`
	Health       string                  `json:"health"`
	HookFailures []PluginHookFailureInfo `json:"hookFailures"`
	InstalledAt  time.Time               `json:"installedAt,omitempty"`
	UpdatedAt    time.Time               `json:"updatedAt,omitempty"`
}

type PluginInspection struct {
	Token            string             `json:"token"`
	ExpiresAt        time.Time          `json:"expiresAt"`
	Source           string             `json:"source"`
	Mode             string             `json:"mode"`
	Digest           string             `json:"digest"`
	RequiresApproval bool               `json:"requiresApproval"`
	Plugin           PluginInfo         `json:"plugin"`
	Diagnostics      []PluginDiagnostic `json:"diagnostics"`
}

type PluginInspectParams struct {
	Source string `json:"source"`
	Mode   string `json:"mode,omitempty"`
}

type PluginInstallParams struct {
	Token     string `json:"token"`
	ProjectID string `json:"projectId,omitempty"`
}

type PluginListParams struct {
	ProjectID string `json:"projectId,omitempty"`
}
type PluginListResult struct {
	Plugins []PluginInfo `json:"plugins"`
}
type PluginGetParams struct {
	PluginID  string `json:"pluginId"`
	ProjectID string `json:"projectId,omitempty"`
}
type PluginIDParams struct {
	PluginID string `json:"pluginId"`
}
type PluginSetEnabledParams struct {
	PluginID  string `json:"pluginId"`
	ProjectID string `json:"projectId"`
	Enabled   bool   `json:"enabled"`
}
type PluginConfigureParams struct {
	PluginID  string            `json:"pluginId"`
	ProjectID string            `json:"projectId"`
	Settings  map[string]any    `json:"settings"`
	Secrets   map[string]string `json:"secrets,omitempty"`
}
type PluginUninstallParams struct {
	PluginID        string `json:"pluginId"`
	DisableProjects bool   `json:"disableProjects"`
}

type PluginErrorData struct {
	Phase       string `json:"phase"`
	Code        string `json:"code"`
	PluginID    string `json:"pluginId,omitempty"`
	Component   string `json:"component,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}
