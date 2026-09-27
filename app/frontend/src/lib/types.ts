// Mirrors internal/protocol (Go). Keep field names in sync with the JSON tags there.

export type Complexity = '' | 'auto' | 'quick' | 'standard' | 'deep';

export interface ModelSelection {
  provider?: string;
  model?: string;
  complexity?: Complexity;
  credentialId?: string;
}

export interface UsageTotals {
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  costUsd: number;
  requests: number;
  estimated?: boolean;
}

export interface ProjectTools {
  shell?: boolean;
  network?: boolean;
  compute?: boolean;
	visualQa?: boolean;
	computerUse?: boolean;
  computeVcpus?: number;
  computeMemoryMiB?: number;
  computeDiskMiB?: number;
  git?: boolean;
  mcpServers?: string[];
}

export interface VCSInfo {
  kind: string;
  hasCommit: boolean;
  branch?: string;
  dirty: number;
  remote?: string;
}

export interface Project {
  id: string;
  name: string;
  root: string;
  instructionsPath?: string;
  settings: ModelSelection;
  tools: ProjectTools;
  archived?: boolean;
  missing?: boolean;
  vcs?: VCSInfo;
  threads: number;
  createdAt: string;
  lastOpenedAt: string;
}

export interface FileEntry {
  name: string;
  path: string;
  dir?: boolean;
  symlink?: boolean;
  size?: number;
  modTime?: string;
}

export type FileAction = 'created' | 'modified' | 'deleted';

export interface FileChangeData {
  path: string;
  action: FileAction;
  turnId?: string;
  additions: number;
  deletions: number;
  diff?: string;
  truncated?: boolean;
  revertable?: boolean;
}

export interface ProjectFilesResult {
  path: string;
  entries: FileEntry[];
  truncated?: boolean;
}

export interface ProjectFileContent {
  path: string;
  content: string;
  bytes: number;
  truncated?: boolean;
  binary?: boolean;
  modTime: string;
}

export interface RevertResult {
  reverted: string[];
  skipped?: string[];
  reason?: string;
}

export interface TaskWorkspaceResult {
  changedFiles?: number;
  message: string;
}

export interface Thread {
  id: string;
  title: string;
  projectId?: string;
  workspaceMode: 'local' | 'worktree';
  channel: string;
  pinned: boolean;
  archived: boolean;
  settings: ModelSelection;
  forkedFrom?: string;
  usage: UsageTotals;
  createdAt: string;
  updatedAt: string;
}

export type TurnStatus = 'running' | 'completed' | 'failed' | 'interrupted';

export interface Turn {
  id: string;
  threadId: string;
  status: TurnStatus;
  selection: ModelSelection;
  resolved: ModelSelection;
  autoPicked?: boolean;
  error?: string;
  usage: UsageTotals;
  /** Every route the turn tried, in order; more than one means it switched. */
  routeTrail?: RouteStep[];
  startedAt: string;
  finishedAt?: string;
}

/** One way to serve a request: a model, the key that pays for it, and why. */
export interface RouteInfo {
  provider: string;
  model: string;
  displayName?: string;
  credentialId: string;
  credentialLabel?: string;
  why: string;
  costPerMTok?: number;
  /** Set when the route cannot be used right now; the text says why. */
  unavailable?: string;
  cooldownEnd?: string;
}

export interface RouteStep extends RouteInfo {
  at: string;
  status: string;
  error?: string;
  latencyMs?: number;
}

export interface ModelRouteResult {
  chosen?: RouteInfo;
  alternatives: RouteInfo[];
  complexity: Complexity;
  autoPicked?: boolean;
  reason?: string;
}

export interface ModelHealthRow {
  provider: string;
  model: string;
  credentialId: string;
  credentialLabel?: string;
  cooldownEnd?: string;
  lastStatus?: string;
  lastError?: string;
  okCount: number;
  errCount: number;
  latencyMs?: number;
  updatedAt: string;
}

export interface ConfiguredModel {
  id: string;
  name: string;
  provider: string;
  model: string;
  enabled: boolean;
}

export interface ModelPool {
  id: string;
  name: string;
  strategy: 'priority' | 'balanced' | 'quality' | 'fast' | 'cheap';
  models: string[];
  enabled: boolean;
}

export interface RoutingConfig {
  models: ConfiguredModel[];
  pools: ModelPool[];
  defaultPool?: string;
}

export interface RouteChangedEvent {
  threadId: string;
  turnId: string;
  from: RouteInfo;
  to: RouteInfo;
  reason: string;
}

export type ItemKind =
  | 'userMessage'
  | 'agentMessage'
  | 'reasoning'
  | 'toolCall'
  | 'fileChange'
  | 'inboundEvent'
  | 'approval'
  | 'contextCompaction'
  | 'error';
export type ItemStatus = 'inProgress' | 'completed' | 'failed' | 'denied';

export interface ToolCallData {
  callId: string;
  name: string;
  args: unknown;
  output?: string;
  error?: string;
  risk?: string;
}

export interface Item {
  id: string;
  threadId: string;
  turnId: string;
  seq: number;
  kind: ItemKind;
  status: ItemStatus;
  text?: string;
  tool?: ToolCallData;
  data?: unknown;
  createdAt: string;
}

export interface Attachment {
  name: string;
  mimeType: string;
  dataB64: string;
}

export interface Approval {
  id: string;
  threadId: string;
  turnId: string;
  itemId: string;
  tool: string;
  args: unknown;
  risk: string;
  reason: string;
  actionSummary: string;
  status: 'pending' | 'approved' | 'denied' | 'expired';
  decidedBy?: string;
  createdAt: string;
  expiresAt: string;
  decidedAt?: string;
}

export interface Credential {
  id: string;
  provider: string;
  label: string;
  baseUrl?: string;
  last4: string;
  enabled: boolean;
  isDefault: boolean;
  fallback: boolean;
  monthlyBudgetUsd?: number;
  hardStop?: boolean;
  lastTestedAt?: string;
  lastTestOk?: boolean;
  createdAt: string;
  monthUsage?: UsageTotals;
}

export interface Model {
  provider: string;
  id: string;
  displayName: string;
  contextWindow?: number;
  supportsTools: boolean;
  supportsImages: boolean;
  supportsReasoning: boolean;
  inputPerMTok: number;
  cachedInputPerMTok: number;
  outputPerMTok: number;
  hidden: boolean;
  source: string;
}

export interface Provider {
  id: string;
  displayName: string;
  enabled: boolean;
  defaultModel: string;
  credentials: number;
}

export interface ComplexityPreset {
  level: Complexity;
  reasoning: string;
  maxToolSteps: number;
  multiAgent: string;
  maxOutputTokens: number;
}

export interface ComplexityDefaults {
  default: Complexity;
  presets: ComplexityPreset[];
  limits: ExecutionLimits;
}

export interface ExecutionLimits {
  maxDurationMinutes: number;
  maxTokens: number;
  maxCostUsd: number;
  maxToolRounds: number;
}

export interface EngineStatus {
  engineVersion: string;
  protocolVersion: string;
  startedAt: string;
  clients: number;
  activeTurns: number;
  pendingApprovals: number;
  dbPath: string;
}

export interface PreviewSession {
  id: string;
  threadId: string;
  projectId: string;
  title: string;
  url: string;
  status: 'starting' | 'ready' | 'stopped';
  output?: string;
}

export interface PreviewEvent {
  preview: PreviewSession;
  output?: string;
  error?: string;
}

export interface BrowserArtifact {
  path: string;
  kind: string;
  mime_type?: string;
  bytes: number;
}

export interface BrowserVerificationResult {
  status: 'passed' | 'failed' | 'blocked' | 'not_run';
  framework?: string;
  command?: string;
  directory?: string;
  duration_ms?: number;
  exit_code?: number;
  output?: string;
  diagnostics: string[];
  artifacts: BrowserArtifact[];
  reason?: string;
	session_id?: string;
	snapshot?: string;
}

export interface ProjectArtifactContent {
  path: string;
  mimeType: string;
  dataB64: string;
  bytes: number;
}

export interface SearchHit {
  threadId: string;
  title: string;
  itemId: string;
  snippet: string;
}

export interface UsageRow {
  key: string;
  label: string;
  usage: UsageTotals;
}

export interface UsageSummary {
  rows: UsageRow[];
  total: UsageTotals;
}

export interface CredentialTestResult {
  ok: boolean;
  latencyMs: number;
  error?: string;
  models?: string[];
}

export interface Schedule {
  run_at?: string;
  frequency?: string;
  time?: string;
  minute?: number;
  day_of_week?: string;
  cron?: string;
}

export interface Task {
  id: number;
  name: string;
  prompt: string;
  taskType: 'one_time' | 'periodic';
  schedule: Schedule;
  timezone: string;
  status: 'active' | 'completed' | 'cancelled';
  settings: ModelSelection;
  threadId?: string;
  nextRunAt?: string;
  lastRunAt?: string;
  lastResult?: string;
  lastError?: string;
  createdBy: string;
  createdAt: string;
}

export interface TaskRun {
  id: number;
  taskId: number;
  status: 'running' | 'success' | 'failed';
  turnId?: string;
  startedAt: string;
  finishedAt?: string;
  result?: string;
  error?: string;
}

export interface SkillInfo {
  name: string;
  description: string;
  version?: string;
  runtime: string;
  riskLevel: string;
  scripts: string[];
  dir: string;
  removable: boolean;
  error?: string;
}

export interface MCPServer {
  name: string;
  transport: string;
  status: string;
  error?: string;
  serverName?: string;
  serverVersion?: string;
  tools: string[];
}

export interface ToolInfo {
  name: string;
  description: string;
  schema: unknown;
  source: string;
}

export interface BudgetWarning {
  credentialId: string;
  label: string;
  spentUsd: number;
  budgetUsd: number;
  percent: number;
}
