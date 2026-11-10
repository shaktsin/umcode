import { app } from './app.svelte';
import { errMsg } from '$lib/format';
import { agentActive, type AgentSnapshot, type DelegationUpdated } from '$lib/delegation';
import type { AgentIntegrationReport } from '$lib/types';

const PLAN_RANK = { proposed: 0, approved: 1, superseded: 2 } as const;
type Snapshot = AgentSnapshot & { agentSequences: Record<string, number>; integrationSequence?: number };
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

/** Owned by one ThreadView; task state never crosses columns. */
export class DelegationState {
  threadId = $state<string | null>(null);
  projectId = $state<string | undefined>();
  tasks = $state<Snapshot[]>([]);
  /** What the lead integrated when a task was cancelled, by task id. Session-only: the server does not list it. */
  reports = $state<Record<string, AgentIntegrationReport>>({});
  pending = $state(false);
  error = $state('');
  private generation = 0;
  private request = 0;
  private events = 0;
  get active() { return this.tasks.find(s => agentActive(s.task.state)); }
  async bind(threadId: string | null, projectId?: string) {
    if (threadId === null || this.threadId !== threadId || this.projectId !== projectId) {
      this.generation++; this.request++; this.threadId = threadId; this.projectId = projectId;
      this.tasks = []; this.reports = {}; this.pending = false; this.error = '';
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
        current.children = s.children ?? [];
        // Plans have no version: a listed plan never moves the same plan back in its lifecycle.
        if (s.plan && !(current.plan?.id === s.plan.id && PLAN_RANK[s.plan.state] < PLAN_RANK[current.plan.state])) current.plan = s.plan;
        for (const agent of s.agents ?? []) this.merge({ threadId: id, taskId: s.task.id, agent }, true);
        if (s.integration) this.merge({ threadId: id, taskId: s.task.id, integration: s.integration }, true);
      }
      this.error = '';
    } catch (e) { if (generation === this.generation && request === this.request) this.error = errMsg(e); }
  }
  apply(event: DelegationUpdated) {
    if (event.threadId !== this.threadId) return;
    const unknownTask = !this.tasks.some(s => s.task.id === event.taskId);
    this.events++;
    this.merge(event);
    if (event.agent?.id || event.workflow || unknownTask) void this.hydrate();
  }
  private merge(event: DelegationUpdated, durable = false) {
    const incoming = event.task ? timestamp(event.task.updatedAt) : null;
    if (event.task && (incoming === null || event.task.threadId !== this.threadId)) return;
    let s = this.tasks.find(s => s.task.id === event.taskId);
    if (!s) {
      if (!event.task || event.task.threadId !== this.threadId) return;
      s = { task: event.task, children: [], agentSequences: {}, agents: [] }; this.tasks = [...this.tasks, s];
    }
    const current = timestamp(s.task.updatedAt);
    if (event.task && incoming !== null && current !== null && incoming >= current) { s.task = event.task; if (durable && event.workflow) s.workflow = event.workflow; }
    this.mergeRuntime(s, event, durable);
  }
  /** Runtime agents merge by id (newer version, or same version with a newer sequence); siblings are untouched. */
  private mergeRuntime(s: Snapshot, event: DelegationUpdated, durable: boolean) {
    const sequence = event.sequence ?? 0, agent = event.agent;
    if (agent?.id && agent.taskId === event.taskId) {
      const agents = s.agents ?? [], old = agents.find(a => a.id === agent.id), previous = s.agentSequences[agent.id] ?? 0;
      if (!(old && (agent.version < old.version || agent.version === old.version && sequence <= previous && !durable))) {
        s.agents = old ? agents.map(a => a.id === agent.id ? agent : a) : [...agents, agent];
        s.agentSequences[agent.id] = old?.version === agent.version ? Math.max(sequence, previous) : sequence;
      }
    }
    if (event.integration) {
      const previous = s.integrationSequence;
      // Integration has no version: order by event sequence. Durable (listed) state fills only a gap.
      if (durable ? previous === undefined : previous === undefined || sequence > previous) {
        s.integration = event.integration; s.integrationSequence = sequence;
      }
    }
  }
  submission(): { delegationTaskId?: string } {
    if (this.active) return { delegationTaskId: this.active.task.id };
    return {};
  }
  async cancel(taskId: string, childId?: string) {
    if (!this.threadId || this.pending) return;
    const generation = this.generation; this.pending = true; this.error = '';
    try {
      const r = await app.call<Omit<AgentSnapshot, 'integration'> & { integration?: AgentIntegrationReport }>('delegation/cancel', { threadId: this.threadId, taskId, ...(childId ? { childId } : {}) });
      if (generation !== this.generation) return;
      // A per-agent cancel's integration is partial; only a task-level cancel yields the task report.
      if (r.integration && !childId) this.reports = { ...this.reports, [taskId]: r.integration };
      this.apply({ threadId: this.threadId!, taskId, task: r.task });
      // Hydration includes retained finalization errors; the cancellation reply
      // alone is not authority to clear their same-version partial outcomes.
      await this.hydrate();
    } catch (e) { if (generation === this.generation) { this.error = errMsg(e); app.toast('error', this.error); } }
    finally { if (generation === this.generation) this.pending = false; }
  }
}
