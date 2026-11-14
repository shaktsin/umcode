import type { TrainingCall, TrainingConfig, TrainingContext, TrainingMetrics, TrainingRun, TrainingState } from './trainingTypes';
const identity = (context: TrainingContext | null) => context ? JSON.stringify([context.projectId, context.modelId, context.modelCreatedAt]) : '';
interface Ticket { request: number; context: TrainingContext }
export function trainingMetricEntries(metrics?: TrainingMetrics | null): { label: string; value: string }[] {
  if (!metrics) return [];
  const fields: [keyof TrainingMetrics, string, 'number' | 'percent' | 'integer'][] = [['loss', 'Loss', 'number'], ['mae', 'MAE', 'number'], ['rmse', 'RMSE', 'number'], ['r2', 'R²', 'number'], ['accuracy', 'Accuracy', 'percent'], ['macroF1', 'Macro F1', 'number'], ['perplexity', 'Perplexity', 'number'], ['tokenAccuracy', 'Token accuracy', 'percent'], ['samples', 'Samples', 'integer']];
  return fields.flatMap(([key, label, format]) => {
    const value = metrics[key];
    return typeof value === 'number' && Number.isFinite(value) ? [{ label, value: format === 'percent' ? `${(value * 100).toFixed(2)}%` : format === 'integer' ? value.toLocaleString() : value.toFixed(4) }] : [];
  });
}
/** Polling belongs to this visible page; the engine owns the job's lifetime. */
export class WorkbenchTrainingState {
  runs = $state<TrainingRun[]>([]);
  runId = $state('');
  runtimeAvailable = $state(false);
  maxBatchSize = $state<number | null>(null);
  issues = $state<string[]>([]);
  config = $state<TrainingConfig>({ epochs: 20, batchSize: 16, learningRate: .003, seed: 17 });
  connected = $state(false);
  dirty = $state(false);
  busy = $state(false);
  stage = $state('');
  error = $state('');
  private context: TrainingContext | null = null;
  private request = 0;
  private disposed = false;
  private refreshQueued = false;
  private pendingRunId = '';
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(private call: TrainingCall) {}
  get run() { return this.runs.find(run => run.id === this.runId); }
  get activeRun() { return this.runs.find(run => run.status === 'running'); }
  get canStart() { return this.connected && !this.disposed && !this.dirty && !this.busy && !this.activeRun && this.runtimeAvailable && !this.issues.length && !this.configError(); }
  configError() {
    const { epochs, batchSize, learningRate, seed } = this.config;
    if (!Number.isInteger(epochs) || epochs < 1 || epochs > 1000) return 'Epochs must be a whole number from 1 to 1,000.';
    if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 256) return 'Batch size must be a whole number from 1 to 256.';
    if (this.maxBatchSize === 0) return 'This saved model exceeds the current memory or compute budget even at batch size 1. Free memory or reduce the model.';
    if (this.maxBatchSize !== null && batchSize > this.maxBatchSize) return `Current memory and compute budget supports batch size ${this.maxBatchSize} or less.`;
    if (!Number.isFinite(learningRate) || learningRate < 0.000001 || learningRate > 1) return 'Learning rate must be between 0.000001 and 1.';
    if (!Number.isSafeInteger(seed) || seed < 0 || seed > 2147483647) return 'Seed must be a whole number from 0 to 2,147,483,647.';
    return '';
  }
  private clearTimer() { if (this.timer !== undefined) clearTimeout(this.timer); this.timer = undefined; }
  private invalidate() { this.clearTimer(); this.refreshQueued = false; this.pendingRunId = ''; this.request++; this.busy = false; this.stage = ''; this.error = ''; }
  private begin(stage: string): Ticket | null {
    if (!this.connected || !this.context || this.disposed || this.busy) return null;
    this.clearTimer(); this.busy = true; this.stage = stage; this.error = '';
    return { request: ++this.request, context: { ...this.context } };
  }
  private current(ticket: Ticket) { return !this.disposed && this.connected && ticket.request === this.request && identity(ticket.context) === identity(this.context); }
  private schedule() { this.clearTimer(); if (!this.disposed && this.connected && this.activeRun) this.timer = setTimeout(() => { this.timer = undefined; void this.refresh(); }, 1000); }
  private finish(ticket: Ticket) { if (this.current(ticket)) { this.busy = false; this.stage = ''; if (this.refreshQueued) { this.refreshQueued = false; void this.refresh(); } else this.schedule(); } }
  private fail(ticket: Ticket, error: unknown) { if (this.current(ticket)) this.error = error instanceof Error ? error.message : 'Local training failed. Refresh and try again.'; }
  private apply(state: TrainingState) {
    this.runs = (state.runs ?? []).filter(run => run.modelCreatedAt === this.context?.modelCreatedAt);
    this.runtimeAvailable = state.runtimeAvailable; this.issues = state.issues ?? [];
    this.maxBatchSize = Number.isInteger(state.maxBatchSize) ? state.maxBatchSize! : null;
    if (!this.runs.some(run => run.id === this.runId)) this.runId = this.runs[0]?.id ?? '';
    if (this.pendingRunId && this.runs.some(run => run.id === this.pendingRunId)) { this.runId = this.pendingRunId; this.pendingRunId = ''; }
  }
  private upsert(run: TrainingRun, select = false) {
    if (run.modelCreatedAt !== this.context?.modelCreatedAt) throw new Error('The training run belongs to another model version. Refresh this model.');
    const exists = this.runs.some(item => item.id === run.id);
    this.runs = exists ? this.runs.map(item => item.id === run.id ? run : item) : [run, ...this.runs];
    if (select || !this.runId) this.runId = run.id;
  }
  async activate(context: TrainingContext, connected: boolean, dirty = false) {
    if (this.disposed) return;
    this.dirty = dirty;
    const changed = identity(context) !== identity(this.context), reconnected = connected && !this.connected;
    if (!changed && connected === this.connected) return;
    this.invalidate(); this.context = { ...context }; this.connected = connected;
    if (changed) { this.runs = []; this.runId = ''; this.issues = []; this.runtimeAvailable = false; this.maxBatchSize = null; }
    if (connected && (changed || reconnected)) await this.refresh();
  }
  async refresh() {
    if (this.busy) { this.refreshQueued = true; return; }
    const ticket = this.begin('Checking local runs…'); if (!ticket) return;
    try { const result = await this.call('workbench/training/get', { ...ticket.context }) as TrainingState; if (this.current(ticket)) this.apply(result); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  selectRun(id: string) { this.pendingRunId = ''; if (this.runs.some(run => run.id === id)) this.runId = id; }
  async focusRun(id: string) { if (!id) return; this.pendingRunId = id; await this.refresh(); }
  async start() {
    if (!this.canStart) return;
    const ticket = this.begin('Starting local training…'); if (!ticket) return;
    const config = { ...this.config };
    try { const run = await this.call('workbench/training/start', { ...ticket.context, config }) as TrainingRun; if (this.current(ticket)) this.upsert(run, true); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async stop() {
    const runId = this.activeRun?.id; if (!runId) return;
    const ticket = this.begin('Stopping local training…'); if (!ticket) return;
    try { const run = await this.call('workbench/training/stop', { ...ticket.context, runId }) as TrainingRun; if (this.current(ticket)) this.upsert(run); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  dispose() { this.invalidate(); this.disposed = true; this.context = null; this.connected = false; }
}
