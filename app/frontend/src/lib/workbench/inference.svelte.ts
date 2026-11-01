import { checkpointByteLimit, inferenceBase64, inferenceRuntimes, validateCheckpointFile } from './inference';
import type { ArchitectureTest, Checkpoint, CheckpointFile, InferenceCall, InferenceContext, InferenceState, Prediction, Promotion, RuntimeInfo } from './inferenceTypes';

interface Ticket { request: number; context: InferenceContext }
const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'The local inference operation failed. Try again.';
const identity = (context: InferenceContext | null) => context ? JSON.stringify([context.projectId, context.modelId, context.modelCreatedAt]) : '';

/** Owns one visible model incarnation. Input edits invalidate every outstanding completion and test receipt. */
export class WorkbenchInferenceState {
  checkpoints = $state<Checkpoint[]>([]);
  runtimes = $state<RuntimeInfo[]>(inferenceRuntimes([]));
  checkpointId = $state('');
  runtime = $state('pytorch');
  loadedCheckpointId = $state('');
  promotion = $state<Promotion | null>(null);
  issues = $state<string[]>([]);
  prompt = $state('');
  features = $state<Record<string, string>>({});
  imageBase64 = $state('');
  imageName = $state('');
  temperature = $state(0.7);
  maxNewTokens = $state(64);
  prediction = $state<Prediction | null>(null);
  architecture = $state<ArchitectureTest | null>(null);
  stage = $state('');
  error = $state('');
  notice = $state('');
  connected = $state(false);
  private context: InferenceContext | null = null;
  private request = 0;
  private disposed = false;
  private testedScope = '';

  constructor(private call: InferenceCall, private refreshCatalog: () => Promise<void> = async () => {}) {}
  get checkpoint() { return this.checkpoints.find(item => item.id === this.checkpointId); }
  get busy() { return !!this.stage; }
  get availableRuntime() { return this.runtime === 'pytorch' && this.runtimes.some(item => item.id === this.runtime && item.available); }
  get canPredict() { return this.connected && !this.busy && !!this.checkpoint && this.availableRuntime && !this.inputError(); }
  get canPromote() {
    return this.connected && !this.busy && !!this.checkpoint?.chatCompatible && this.availableRuntime &&
      this.prediction?.checkpointId === this.checkpointId && !!this.prediction?.testId && this.testedScope === this.scope();
  }
  get isPromoted() { return this.promotion?.checkpointId === this.checkpointId && this.promotion?.runtime === this.runtime; }

  private scope() { return JSON.stringify([identity(this.context), this.checkpointId, this.runtime, this.prompt, this.features, this.imageBase64, this.temperature, this.maxNewTokens]); }
  private invalidate() { this.request++; this.stage = ''; this.prediction = null; this.testedScope = ''; this.error = ''; this.notice = ''; }
  private begin(stage: string): Ticket | null {
    if (this.disposed || !this.context || !this.connected) { this.error = 'Engine disconnected. Reconnect to use local inference.'; return null; }
    const ticket = { request: ++this.request, context: { ...this.context } }; this.stage = stage; this.error = ''; this.notice = ''; return ticket;
  }
  private current(ticket: Ticket) { return !this.disposed && this.connected && ticket.request === this.request && identity(ticket.context) === identity(this.context); }
  private finish(ticket: Ticket) { if (this.current(ticket)) this.stage = ''; }
  private fail(ticket: Ticket, error: unknown) { if (this.current(ticket)) this.error = errorMessage(error); }
  private params(ticket: Ticket) { return { ...ticket.context, checkpointId: this.checkpointId, runtime: this.runtime }; }
  private apply(state: InferenceState) {
    this.checkpoints = (state.checkpoints ?? []).filter(item => item.modelCreatedAt === this.context?.modelCreatedAt);
    this.runtimes = inferenceRuntimes(state.runtimes ?? []);
    this.loadedCheckpointId = state.loadedCheckpointId || '';
    this.issues = state.issues ?? [];
    this.promotion = state.promotion && identity(state.promotion) === identity(this.context) ? state.promotion : null;
    if (!this.checkpoints.some(item => item.id === this.checkpointId)) this.checkpointId = this.promotion?.checkpointId || this.checkpoints[0]?.id || '';
  }

  async activate(context: InferenceContext, connected: boolean) {
    if (this.disposed) return;
    const changed = identity(context) !== identity(this.context), reconnected = connected && !this.connected;
    if (!changed && connected === this.connected) return;
    this.invalidate(); this.context = { ...context }; this.connected = connected;
    if (changed) {
      this.checkpoints = []; this.checkpointId = ''; this.runtime = 'pytorch'; this.loadedCheckpointId = ''; this.promotion = null; this.issues = [];
      this.architecture = null; this.prompt = ''; this.features = {}; this.imageBase64 = ''; this.imageName = ''; this.runtimes = inferenceRuntimes([]);
    }
    if (connected && (changed || reconnected)) await this.refresh();
  }
  async refresh() {
    this.invalidate(); const ticket = this.begin('Checking local checkpoints…'); if (!ticket) return;
    try { const state = await this.call('workbench/inference/get', { ...ticket.context }) as InferenceState; if (this.current(ticket)) this.apply(state); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  selectCheckpoint(id: string) {
    if (id === this.checkpointId) return;
    this.invalidate(); this.checkpointId = id; this.prompt = ''; this.features = {}; this.imageBase64 = ''; this.imageName = '';
  }
  selectRuntime(id: string) { if (id !== this.runtime) { this.invalidate(); this.runtime = id; } }
  setPrompt(prompt: string) { if (prompt !== this.prompt) { this.invalidate(); this.prompt = prompt; } }
  featureValue(key: string): string {
    const value = this.features[key];
    return Object.hasOwn(this.features, key) && typeof value === 'string' ? value : '';
  }
  setFeature(key: string, value: string) { if (this.features[key] !== value) { this.invalidate(); this.features = { ...this.features, [key]: value }; } }
  setImage(base64: string, name: string) { this.invalidate(); this.imageBase64 = base64; this.imageName = name; }
  setTemperature(value: number) { if (value !== this.temperature) { this.invalidate(); this.temperature = value; } }
  setMaxNewTokens(value: number) { if (value !== this.maxNewTokens) { this.invalidate(); this.maxNewTokens = value; } }

  inputError(): string {
    const input = this.checkpoint?.input;
    if (!input) return 'Import and select a checkpoint first.';
    if (input.mode === 'text' && !this.prompt.trim()) return 'Enter a prompt to test this checkpoint.';
    if (input.mode === 'text' && new TextEncoder().encode(this.prompt).length > 8192) return 'Prompt must be 8,192 UTF-8 bytes or smaller.';
    if (input.mode === 'features' && input.features.some(key => !this.featureValue(key).trim() || !Number.isFinite(Number(this.featureValue(key))))) return 'Enter a finite number for every input feature.';
    if (input.mode === 'image' && !this.imageBase64) return 'Choose a PNG or JPEG image.';
    if (!['text', 'features', 'image'].includes(input.mode)) return `Unsupported checkpoint input mode: ${input.mode}.`;
    if (!Number.isFinite(this.temperature) || this.temperature < 0 || this.temperature > 2) return 'Temperature must be between 0 and 2.';
    if (!Number.isInteger(this.maxNewTokens) || this.maxNewTokens < 1 || this.maxNewTokens > 256) return 'Output tokens must be a whole number from 1 to 256.';
    return '';
  }
  async testArchitecture() {
    if (this.busy) return;
    this.invalidate(); this.architecture = null;
    const ticket = this.begin('Testing saved architecture with random weights…'); if (!ticket) return;
    try { const result = await this.call('workbench/inference/test-architecture', { ...ticket.context }, 120_000) as ArchitectureTest; if (this.current(ticket)) this.architecture = result; }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async importImage(file: CheckpointFile) {
    if (this.busy) return;
    if (!/\.(png|jpe?g|webp|bmp)$/i.test(file.name)) { this.error = 'Choose a PNG, JPEG, WEBP or BMP image.'; return; }
    if (!Number.isSafeInteger(file.size) || file.size <= 0 || file.size > 8 * 1024 * 1024) { this.error = 'Images must be nonempty and at most 8 MiB.'; return; }
    this.invalidate(); const ticket = this.begin('Reading local image…'); if (!ticket) return;
    try {
      const bytes = await file.arrayBuffer(); if (!this.current(ticket)) return;
      if (bytes.byteLength !== file.size) throw new Error('Image changed while reading. Choose it again.');
      this.imageBase64 = inferenceBase64(bytes); this.imageName = file.name;
    } catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async importCheckpoint(file: CheckpointFile, name: string) {
    if (this.busy) return;
    const invalid = validateCheckpointFile(file); if (invalid) { this.error = invalid; return; }
    if (!name.trim()) { this.error = 'Name this checkpoint before importing.'; return; }
    this.invalidate(); const ticket = this.begin('Importing and validating saved weights…'); if (!ticket) return;
    try {
      const bytes = await file.arrayBuffer(); if (!this.current(ticket)) return;
      if (bytes.byteLength > checkpointByteLimit || bytes.byteLength !== file.size) throw new Error('Checkpoint file size changed or exceeds 64 MiB. Choose the file again.');
      const imported = await this.call('workbench/inference/import', { ...ticket.context, name: name.trim(), filename: file.name, contentBase64: inferenceBase64(bytes) }, 120_000) as Checkpoint;
      if (!this.current(ticket)) return;
      if (imported.modelCreatedAt !== ticket.context.modelCreatedAt) throw new Error('This checkpoint belongs to a different model version. Refresh the model.');
      this.checkpoints = [imported, ...this.checkpoints.filter(item => item.id !== imported.id)]; this.checkpointId = imported.id;
      this.prompt = ''; this.features = {}; this.imageBase64 = ''; this.imageName = ''; this.notice = 'Checkpoint imported with its saved architecture and Data preparation.';
    } catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async load() {
    if (this.busy) return;
    if (!this.checkpoint || !this.availableRuntime) { this.error = 'Select a checkpoint and an available PyTorch runtime.'; return; }
    this.invalidate(); const ticket = this.begin('Loading checkpoint locally…'); if (!ticket) return;
    try { const state = await this.call('workbench/inference/load', this.params(ticket), 120_000) as InferenceState; if (this.current(ticket)) this.apply(state); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async unload() {
    if (this.busy) return;
    this.invalidate(); const ticket = this.begin('Unloading local checkpoint…'); if (!ticket) return;
    try { const state = await this.call('workbench/inference/unload', { ...ticket.context }, 120_000) as InferenceState; if (this.current(ticket)) this.apply(state); }
    catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async predict() {
    if (this.busy) return;
    const inputError = this.inputError(); if (inputError) { this.prediction = null; this.testedScope = ''; this.error = inputError; return; }
    if (!this.availableRuntime) { this.error = 'Select an available PyTorch runtime.'; return; }
    this.invalidate(); const ticket = this.begin('Loading and testing locally…'); if (!ticket) return;
    const params = this.params(ticket), scope = this.scope(), input = this.checkpoint!.input;
    const payload = input.mode === 'text' ? { prompt: this.prompt } : input.mode === 'features' ? { features: Object.fromEntries(input.features.map(key => [key, Number(this.featureValue(key))])) } : { imageBase64: this.imageBase64 };
    try {
      if (this.loadedCheckpointId !== params.checkpointId) {
        const state = await this.call('workbench/inference/load', params, 120_000) as InferenceState; if (!this.current(ticket)) return;
        this.apply(state);
        if (this.loadedCheckpointId !== params.checkpointId) throw new Error('The selected checkpoint did not load. Review the runtime issues and try again.');
      }
      const result = await this.call('workbench/inference/predict', { ...params, ...payload, temperature: this.temperature, maxNewTokens: this.maxNewTokens }, 120_000) as Prediction;
      if (!this.current(ticket) || scope !== this.scope()) return;
      if (result.checkpointId !== this.checkpointId || !result.testId) throw new Error('The runner returned no matching test receipt. Run this checkpoint again.');
      this.prediction = result; this.testedScope = scope;
    } catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async promote() {
    if (!this.canPromote) return;
    const testId = this.prediction!.testId; const ticket = this.begin('Saving checkpoint in Chat…'); if (!ticket) return;
    try {
      const result = await this.call('workbench/inference/promote', { ...this.params(ticket), testId }) as Promotion;
      if (!this.current(ticket)) return;
      if (identity(result) !== identity(ticket.context) || result.checkpointId !== this.checkpointId || result.runtime !== this.runtime) throw new Error('Promotion does not match the tested checkpoint. Refresh and try again.');
      this.promotion = result; this.notice = 'This checkpoint is available in your regular chat picker.';
      await this.refreshCatalog();
    } catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  async unpromote() {
    if (this.busy || !this.promotion) return;
    const ticket = this.begin('Removing model from Chat…'); if (!ticket) return;
    try {
      await this.call('workbench/inference/unpromote', { ...ticket.context }); if (!this.current(ticket)) return;
      this.promotion = null; this.notice = 'Removed from Chat. Checkpoint weights are still saved in this project.'; await this.refreshCatalog();
    } catch (error) { this.fail(ticket, error); } finally { this.finish(ticket); }
  }
  dispose() { this.invalidate(); this.disposed = true; this.context = null; this.connected = false; }
}
