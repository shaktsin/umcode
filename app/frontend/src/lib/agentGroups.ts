import type { RuntimeAgent } from './types';
import { agentStateLabel } from './delegation';

export type AgentToneName = 'running' | 'attention' | 'failed' | 'stopped' | 'waiting' | 'done' | 'neutral';
export interface AgentTone { tone: AgentToneName; label: string }
export interface Attempt { agent: RuntimeAgent; attempt: number }
export interface AgentGroup { key: string; depth: number; latest: RuntimeAgent; attempts: (Attempt & { id: string })[] }

const budgetStopped = (a: Pick<RuntimeAgent, 'state' | 'result'>) => agentStateLabel(a) === 'Budget stopped';

/** One state per agent: red is for real failures; a budget stop is a calm amber of its own. */
export function agentTone(a: RuntimeAgent): AgentTone {
  if (budgetStopped(a)) return { tone: 'stopped', label: 'Stopped by budget' };
  switch (a.state) {
    case 'running': return { tone: 'running', label: 'Running' };
    case 'waiting_approval': return { tone: 'attention', label: 'Waiting for approval' };
    case 'queued': return { tone: 'waiting', label: 'Waiting to start' };
    case 'completed': return { tone: 'done', label: 'Completed' };
    case 'failed': return { tone: 'failed', label: 'Failed' };
    case 'cancelled': return { tone: 'neutral', label: 'Cancelled' };
    default: return { tone: 'neutral', label: 'Interrupted' };
  }
}

/** Short status for one line of the attempts list. */
export function attemptLabel(a: RuntimeAgent): string {
  return budgetStopped(a) ? 'Stopped' : agentTone(a).label;
}

/**
 * Collapses retries into one group per plan entry within a task. Agents without a plan entry are never merged.
 * Input is the pre-order tree rows (oldest first); attempts come back newest first, numbered from the oldest.
 */
export function groupAgents(rows: { agent: RuntimeAgent; depth: number }[]): AgentGroup[] {
  const groups = new Map<string, { depth: number; agents: RuntimeAgent[] }>();
  for (const { agent, depth } of rows) {
    const key = agent.planEntry ? `${agent.taskId}:${agent.planEntry}` : `id:${agent.id}`;
    const g = groups.get(key);
    if (g) g.agents.push(agent); else groups.set(key, { depth, agents: [agent] });
  }
  return [...groups].map(([key, g]) => ({
    key, depth: g.depth, latest: g.agents[g.agents.length - 1],
    attempts: g.agents.map((agent, i) => ({ agent, attempt: i + 1, id: agent.id })).reverse(),
  }));
}

/** What the agent may do, in words rather than permission codes, ending with the model it runs on. */
export function permissionLine(a: RuntimeAgent): string {
  const c = a.capabilities, parts: string[] = [];
  if (c.files === 'read-write') parts.push('Edits files'); else if (c.files === 'read') parts.push('Reads files');
  if (c.shell === 'read-write') parts.push('Runs commands'); else if (c.shell === 'read-only') parts.push('Read-only shell');
  if (c.network) parts.push('Network access');
  if (c.spawnMax > 0) parts.push('Can start agents');
  if (!parts.length) parts.push('No tool access');
  parts.push(a.modelRequested || 'Model not recorded');
  return parts.join(' · ');
}

const ORDER: { tone: AgentToneName; word: string }[] = [
  { tone: 'running', word: 'running' }, { tone: 'attention', word: 'need approval' }, { tone: 'failed', word: 'failed' },
  { tone: 'stopped', word: 'stopped' }, { tone: 'waiting', word: 'waiting' }, { tone: 'done', word: 'completed' },
];
/** Header status: the latest attempt of each group, counted in a fixed order, zero counts omitted. */
export function statusCounts(groups: AgentGroup[]): { tone: AgentToneName; label: string }[] {
  const n = new Map<AgentToneName, number>();
  for (const g of groups) { const t = agentTone(g.latest).tone; n.set(t, (n.get(t) ?? 0) + 1); }
  return ORDER.filter(o => n.get(o.tone)).map(o => ({ tone: o.tone, label: `${n.get(o.tone)} ${o.word}` }));
}

/** A task stopped by a limit can be fixed in settings; a user cancel cannot. */
export const taskStopNeedsSettings = (reason: string) => /budget|limit|allowance|token|cost/i.test(reason);

/** The blocker as a sentence, or '' when the agent recorded none. */
export function reasonOf(a: Pick<RuntimeAgent, 'result'>): string {
  const r = (a.result.blocker || '').trim();
  return r ? r[0].toUpperCase() + r.slice(1) : '';
}
