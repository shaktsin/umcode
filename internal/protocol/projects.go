package protocol

import "time"

// Project methods (client → engine).
const (
	MethodProjectList             = "project/list"
	MethodProjectCreate           = "project/create"
	MethodProjectOpen             = "project/open"
	MethodProjectUpdate           = "project/update"
	MethodProjectDelete           = "project/delete"
	MethodProjectInstructions     = "project/instructions"
	MethodProjectScanInstructions = "project/instructions/scan"
	MethodProjectFiles            = "project/files"
	MethodProjectReadFile         = "project/readFile"
	MethodProjectReadArtifact     = "project/readArtifact"
	MethodProjectDiff             = "project/diff"
	MethodProjectRevertTurn       = "project/revertTurn"
	MethodProjectKeepTask         = "project/keepTaskChanges"
	MethodProjectDiscardTask      = "project/discardTaskWorkspace"

	// NotifyProjectUpdated is sent when a project is created, changed or deleted.
	NotifyProjectUpdated = "project/updated"
)

// ProjectTools says which tool groups are available inside a project.
// A nil field means "use the engine default".
type ProjectTools struct {
	Shell       *bool `json:"shell,omitempty"`
	Network     *bool `json:"network,omitempty"`     // network access from shell commands
	Compute     *bool `json:"compute,omitempty"`     // run shell commands in an app-bundled microVM
	VisualQA    *bool `json:"visualQa,omitempty"`    // let the agent control the isolated preview browser
	ComputerUse *bool `json:"computerUse,omitempty"` // let the agent control explicitly selected desktop apps
	// ComputerUseApps restricts Computer Use to these apps: matched
	// case-insensitively against app_name, bundle_id, or app_path (basename).
	// Empty or nil means no restriction (every app is allowed).
	ComputerUseApps  []string `json:"computerUseApps,omitempty"`
	ComputeVCPUs     *int     `json:"computeVcpus,omitempty"`
	ComputeMemoryMiB *int     `json:"computeMemoryMiB,omitempty"`
	ComputeDiskMiB   *int     `json:"computeDiskMiB,omitempty"`
	Git              *bool    `json:"git,omitempty"`
	// MCPServers, when set, limits which configured MCP servers this project
	// may use. An empty slice means none; nil means all of them.
	MCPServers []string `json:"mcpServers,omitempty"`
}

// VCSInfo is what the engine could learn about a project's checkout.
type VCSInfo struct {
	Kind      string `json:"kind"` // git, or "" when the folder is not a repo
	HasCommit bool   `json:"hasCommit"`
	Branch    string `json:"branch,omitempty"`
	Dirty     int    `json:"dirty"` // files with uncommitted changes
	Remote    string `json:"remote,omitempty"`
}

// Project is a folder the agent may work in, plus its settings. The root is
// the sandbox boundary: tools refuse paths outside it.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
	// InstructionsPath is retained for older project records; UMCODE writes only UMCODE.md.
	InstructionsPath string         `json:"instructionsPath,omitempty"`
	Settings         ModelSelection `json:"settings"`
	Tools            ProjectTools   `json:"tools"`
	Archived         bool           `json:"archived,omitempty"`
	// Missing is true when the folder is gone from disk.
	Missing      bool      `json:"missing,omitempty"`
	VCS          *VCSInfo  `json:"vcs,omitempty"`
	Threads      int       `json:"threads"`
	CreatedAt    time.Time `json:"createdAt"`
	LastOpenedAt time.Time `json:"lastOpenedAt"`
}

type ProjectListParams struct {
	IncludeArchived bool `json:"includeArchived,omitempty"`
}

type ProjectListResult struct {
	Projects []Project `json:"projects"`
}

type ProjectCreateParams struct {
	Root     string         `json:"root"`
	Name     string         `json:"name,omitempty"`
	Settings ModelSelection `json:"settings,omitempty"`
	Tools    ProjectTools   `json:"tools,omitempty"`
}

type ProjectIDParams struct {
	ProjectID string `json:"projectId"`
}

type ProjectUpdateParams struct {
	ProjectID        string          `json:"projectId"`
	Name             *string         `json:"name,omitempty"`
	Root             *string         `json:"root,omitempty"`
	Settings         *ModelSelection `json:"settings,omitempty"`
	Tools            *ProjectTools   `json:"tools,omitempty"`
	InstructionsPath *string         `json:"instructionsPath,omitempty"`
	Archived         *bool           `json:"archived,omitempty"`
}

// InstructionSource is one file that contributed to a project's instructions.
type InstructionSource struct {
	Scope string `json:"scope"` // global | project | nested
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
	Error string `json:"error,omitempty"` // e.g. truncated because it is too large
}

type ProjectInstructionsParams struct {
	ProjectID string `json:"projectId"`
	// Content, when non-nil, writes UMCODE.md before reading it back.
	Content *string `json:"content,omitempty"`
}

type ProjectInstructionsResult struct {
	// Composed is what the engine puts in the system prompt.
	Composed string              `json:"composed"`
	Project  string              `json:"project"` // UMCODE.md text
	Path     string              `json:"path"`    // where a write would go
	Exists   bool                `json:"exists"`
	Sources  []InstructionSource `json:"sources"`
}

// ProjectInstructionDraft is a read-only scan result to review before saving.
type ProjectInstructionDraft struct {
	Content            string   `json:"content"`
	ExistingUMCodeFile bool     `json:"existingUMCodeFile"`
	ScannedFiles       []string `json:"scannedFiles"`
}

// FileEntry is one node of a project's file tree.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // relative to the project root, always slash-separated
	Dir     bool      `json:"dir,omitempty"`
	Symlink bool      `json:"symlink,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modTime,omitempty"`
}

type ProjectFilesParams struct {
	ProjectID string `json:"projectId"`
	ThreadID  string `json:"threadId,omitempty"`
	Path      string `json:"path,omitempty"`  // directory, relative to the root
	Depth     int    `json:"depth,omitempty"` // 1 (default) lists one level
	Limit     int    `json:"limit,omitempty"`
}

type ProjectFilesResult struct {
	Path      string      `json:"path"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated,omitempty"`
}

type ProjectReadFileParams struct {
	ProjectID string `json:"projectId"`
	ThreadID  string `json:"threadId,omitempty"`
	Path      string `json:"path"`
	MaxBytes  int    `json:"maxBytes,omitempty"`
}

type ProjectReadFileResult struct {
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	Bytes     int64     `json:"bytes"`
	Truncated bool      `json:"truncated,omitempty"`
	Binary    bool      `json:"binary,omitempty"`
	ModTime   time.Time `json:"modTime"`
}

type ProjectReadArtifactParams struct {
	ProjectID string `json:"projectId"`
	ThreadID  string `json:"threadId,omitempty"`
	Path      string `json:"path"`
}

type ProjectReadArtifactResult struct {
	Path     string `json:"path"`
	MimeType string `json:"mimeType"`
	DataB64  string `json:"dataB64"`
	Bytes    int64  `json:"bytes"`
}

// File change actions.
const (
	FileCreated  = "created"
	FileModified = "modified"
	FileDeleted  = "deleted"
)

// FileChangeData is the payload of a fileChange item and one row of a diff.
type FileChangeData struct {
	Path   string `json:"path"` // relative to the project root
	Action string `json:"action"`
	// TurnID is the turn that made the change, which project/revertTurn undoes.
	TurnID    string `json:"turnId,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Diff is a unified diff, truncated for very large changes.
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Revertable is false when the previous content was too large to keep.
	Revertable bool `json:"revertable,omitempty"`
}

type ProjectDiffParams struct {
	ProjectID string `json:"projectId,omitempty"`
	// TurnID limits the diff to what one turn changed; empty means every
	// change recorded for the project's threads, newest first.
	TurnID string `json:"turnId,omitempty"`
	Path   string `json:"path,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type ProjectDiffResult struct {
	Files []FileChangeData `json:"files"`
}

type ProjectRevertTurnParams struct {
	TurnID string   `json:"turnId"`
	Paths  []string `json:"paths,omitempty"` // empty = every file the turn changed
}

type ProjectRevertTurnResult struct {
	Reverted []string `json:"reverted"`
	Skipped  []string `json:"skipped,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type TaskWorkspaceParams struct {
	ThreadID string `json:"threadId"`
}

type TaskWorkspaceResult struct {
	ChangedFiles int    `json:"changedFiles,omitempty"`
	Message      string `json:"message"`
}

// ProjectEvent wraps a project for project/updated.
type ProjectEvent struct {
	Project Project `json:"project"`
	Deleted bool    `json:"deleted,omitempty"`
}
