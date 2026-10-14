package protocol

import (
	"encoding/json"
	"time"
)

// Task types.
const (
	TaskOneTime  = "one_time"
	TaskPeriodic = "periodic"
)

// Task statuses.
const (
	TaskActive    = "active"
	TaskCompleted = "completed"
	TaskCancelled = "cancelled"
)

// Schedule is the schedule of a task (same JSON as the Python app).
//
//	one_time:  {"run_at": "2026-09-20T09:00"}            (local time in Timezone, or RFC 3339)
//	periodic:  {"frequency": "hourly", "minute": 15}
//	           {"frequency": "daily",  "time": "09:00"}
//	           {"frequency": "weekly", "day_of_week": "mon", "time": "09:00"}
//	           {"frequency": "cron",   "cron": "*/30 9-17 * * mon-fri"}   (Go engine only)
type Schedule struct {
	RunAt     string `json:"run_at,omitempty"`
	Frequency string `json:"frequency,omitempty"`
	Time      string `json:"time,omitempty"`
	Minute    *int   `json:"minute,omitempty"`
	DayOfWeek string `json:"day_of_week,omitempty"`
	Cron      string `json:"cron,omitempty"`
}

// Task is a scheduled task.
type Task struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Prompt     string         `json:"prompt"`
	TaskType   string         `json:"taskType"`
	Schedule   Schedule       `json:"schedule"`
	Timezone   string         `json:"timezone"`
	Status     string         `json:"status"`
	Settings   ModelSelection `json:"settings"`
	ThreadID   string         `json:"threadId,omitempty"`
	NextRunAt  *time.Time     `json:"nextRunAt,omitempty"`
	LastRunAt  *time.Time     `json:"lastRunAt,omitempty"`
	LastResult string         `json:"lastResult,omitempty"`
	LastError  string         `json:"lastError,omitempty"`
	CreatedBy  string         `json:"createdBy"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// TaskRun is one execution of a task.
type TaskRun struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"taskId"`
	Status     string     `json:"status"` // running | success | failed
	TurnID     string     `json:"turnId,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Result     string     `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Task methods.
const (
	MethodTaskList   = "task/list"
	MethodTaskCreate = "task/create"
	MethodTaskCancel = "task/cancel"
	MethodTaskRuns   = "task/runs"
	MethodTaskRunNow = "task/runNow"

	MethodSkillList    = "skill/list"
	MethodSkillGet     = "skill/get"
	MethodSkillInstall = "skill/install"
	MethodSkillRemove  = "skill/remove"

	MethodMCPList    = "mcp/list"
	MethodMCPRestart = "mcp/restart"
	MethodToolList   = "tool/list"

	NotifyTaskUpdated = "task/updated"
)

type TaskListParams struct {
	Status string `json:"status,omitempty"`
}

type TaskListResult struct {
	Tasks []Task `json:"tasks"`
}

type TaskCreateParams struct {
	Name     string         `json:"name"`
	Prompt   string         `json:"prompt"`
	TaskType string         `json:"taskType"`
	Schedule Schedule       `json:"schedule"`
	Timezone string         `json:"timezone,omitempty"` // IANA name; default: the engine's local zone
	Settings ModelSelection `json:"settings,omitempty"`
}

type TaskIDParams struct {
	TaskID int64 `json:"taskId"`
}

type TaskRunsResult struct {
	Runs []TaskRun `json:"runs"`
}

type TaskEvent struct {
	Task Task `json:"task"`
}

// SkillInfo describes an installed skill.
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version,omitempty"`
	Runtime     string   `json:"runtime"`
	RiskLevel   string   `json:"riskLevel"`
	Scripts     []string `json:"scripts"`
	Dir         string   `json:"dir"`
	Removable   bool     `json:"removable"`
	Error       string   `json:"error,omitempty"`
}

type SkillListResult struct {
	Skills []SkillInfo `json:"skills"`
	Dirs   []string    `json:"dirs"`
}

type SkillListParams struct {
	ProjectID string `json:"projectId,omitempty"`
}

type SkillNameParams struct {
	Name      string `json:"name"`
	ProjectID string `json:"projectId,omitempty"`
}

type SkillGetResult struct {
	Skill        SkillInfo       `json:"skill"`
	Instructions string          `json:"instructions"`
	ScriptsJSON  json.RawMessage `json:"scriptsDetail,omitempty"`
}

type SkillInstallParams struct {
	Source string `json:"source"` // local folder or git URL
	Name   string `json:"name,omitempty"`
}

type MCPServer struct {
	Name          string   `json:"name"`
	Transport     string   `json:"transport"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	ServerName    string   `json:"serverName,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	Tools         []string `json:"tools"`
}

type MCPListResult struct {
	Servers []MCPServer `json:"servers"`
}

type MCPRestartParams struct {
	Name string `json:"name"`
}

type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Source      string          `json:"source"` // builtin | skill | mcp | task
}

type ToolListResult struct {
	Tools []ToolInfo `json:"tools"`
}
