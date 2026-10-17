import { app } from './app.svelte';
import { errMsg } from '$lib/format';
import { agentActive, type AgentSnapshot, type DelegationUpdated } from '$lib/delegation';

type Snapshot = AgentSnapshot & { childErrors: Record<string, string>; childSequences: Record<string, number> };
// Go emits RFC3339 timestamps with variable fractional precision. Compare exact
// instants, keeping nanoseconds that Date.parse would truncate to milliseconds.
function timestamp(value: string): bigint | null {
  const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(value);
  if (!m) return null;
  const seconds = Date.parse(m[1] + 'Z');
  if (!Number.isFinite(seconds) || new Date(seconds).toISOString().slice(0, 19) !== m[1]) return null;
  let offset = 0;
  if (m[3] !== 'Z') {
    const hours = Number(m[3].slice(1, 3)), minutes = Number(m[3].slice(4, 6));
    if (hours > 23 || minutes > 59) return null;
    offset = (hours * 60 + minutes) * 60000 * (m[3][0] === '+' ? 1 : -1);
  }
  return BigInt(seconds - offset) * 1000000n + BigInt((m[2] ?? '').padEnd(9, '0'));
}

/** Owned by one ThreadView; no activation permission or results cross columns. */
export class DelegationState {
  threadId = $state<string | null>(null);
  projectId = $state<string | undefined>();
  tasks = $state<Snapshot[]>([]);
  token = $state<{ activationToken: string; expiresAt: string } | null>(null);
  pending = $state(false);
  error = $state('');
  private generation = 0;
  private request = 0;
  private events = 0;
  get active() { return this.tasks.find(s => agentActive(s.task.state)); }
  async bind(threadId: string | null, projectId?: string, preservePending = false) {
    if (threadId === null || this.threadId !== threadId || this.projectId !== projectId) {
      this.generation++; this.request++; this.threadId = threadId; this.projectId = projectId;
      this.tasks = []; this.token = null; this.pending = preservePending; this.error = '';
    }
    if (threadId) await this.hydrate();
  }
  async hydrate() {
    const id = this.threadId, generation = this.generation, request = ++this.request, events = this.events;
    if (!id) return;
    try {
      const r = await app.call<{ tasks: AgentSnapshot[] }>('delegation/list', { threadId: id });
      if (generation !== this.generation || request !== this.request || events !== this.events) return;
      // Merge through the event guard: a delayed pre-reconnect notification can
      // neither downgrade a durable version nor restore a cleared finalization error.
      for (const s of r.tasks ?? []) {
        if (s.task.threadId !== id) continue;
        this.merge({ threadId: id, taskId: s.task.id, task: s.task, workflow: s.workflow }, true);
        const current = this.tasks.find(t => t.task.id === s.task.id);
        if (!current) continue;
        current.usage = s.usage; current.budgetAvailable = s.budgetAvailable; current.stopReason = s.stopReason;
        for (const child of s.children ?? []) this.merge({ threadId: id, taskId: s.task.id, child, sequence: s.childSequences?.[child.id], error: s.childErrors?.[child.id] }, true);
      }
      this.error = '';
    } catch (e) { if (generation === this.generation && request === this.request) this.error = errMsg(e); }
  }
  apply(event: DelegationUpdated) {
    if (event.threadId !== this.threadId) return;
    this.events++;
    this.merge(event);
    if (event.child?.id || event.workflow || !this.tasks.some(s => s.task.id === event.taskId)) void this.hydrate();
  }
  private merge(event: DelegationUpdated, durable = false) {
    const incoming = event.task ? timestamp(event.task.updatedAt) : null;
    if (event.task && (incoming === null || event.task.threadId !== this.threadId)) return;
    let s = this.tasks.find(s => s.task.id === event.taskId);
    if (!s) {
      if (!event.task || event.task.threadId !== this.threadId) return;
      s = { task: event.task, children: [], childErrors: {}, childSequences: {} }; this.tasks = [...this.tasks, s];
    }
    const current = timestamp(s.task.updatedAt);
    if (event.task && incoming !== null && current !== null && incoming >= current) { s.task = event.task; if (durable && event.workflow) s.workflow = event.workflow; }
    const child = event.child;
    if (!child?.id || child.taskId !== event.taskId) return;
    const old = s.children.find(c => c.id === child.id), sequence = event.sequence ?? 0, previous = s.childSequences[child.id] ?? 0;
    if (old && (child.version < old.version || child.version === old.version && sequence <= previous && !durable)) return;
    s.children = old ? s.children.map(c => c.id === child.id ? child : c) : [...s.children, child];
    s.childSequences[child.id] = old?.version === child.version ? Math.max(sequence, previous) : sequence;
    if (event.error) s.childErrors[child.id] = event.error; else delete s.childErrors[child.id];
  }
  async enable() {
    if (!this.threadId || this.pending || this.active) return;
    const generation = this.generation; this.pending = true; this.error = ''; this.token = null;
    try {
      const token = await app.call<{ activationToken: string; expiresAt: string }>('delegation/enable', { threadId: this.threadId });
      if (generation === this.generation) this.token = token;
    } catch (e) { if (generation === this.generation) this.error = errMsg(e); }
    finally { if (generation === this.generation) this.pending = false; }
  }
  submission(): { delegationTaskId?: string; delegationActivationToken?: string } {
    if (this.active) return { delegationTaskId: this.active.task.id };
    const token = this.token; this.token = null;
    if (!token) return {};
    if (Date.parse(token.expiresAt) <= Date.now()) { this.error = 'Activation expired. Enable agents again.'; throw new Error(this.error); }
    return { delegationActivationToken: token.activationToken };
  }
  async cancel(taskId: string, childId?: string) {
    if (!this.threadId || this.pending) return;
    const generation = this.generation; this.pending = true; this.error = '';
    try {
      const r = await app.call<AgentSnapshot>('delegation/cancel', { threadId: this.threadId, taskId, ...(childId ? { childId } : {}) });
      if (generation !== this.generation) return;
      this.apply({ threadId: this.threadId!, taskId, task: r.task });
      // Hydration includes retained finalization errors; the cancellation reply
      // alone is not authority to clear their same-version partial outcomes.
      await this.hydrate();
    } catch (e) { if (generation === this.generation) { this.error = errMsg(e); app.toast('error', this.error); } }
    finally { if (generation === this.generation) this.pending = false; }
  }
}
