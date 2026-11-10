import type { AgentNode } from './agentTree';
import type { AgentIntegrationReport, AgentIntegrationStatus, RuntimeAgent } from './types';

/** Pre-order rows of the agent forest. Agent objects are passed through untouched so a keyed row only re-renders when its own agent changes. */
export function flattenTree(nodes: AgentNode[]): { agent: RuntimeAgent; depth: number }[] {
  const rows: { agent: RuntimeAgent; depth: number }[] = [];
  const walk = (n: AgentNode) => { rows.push({ agent: n.agent, depth: n.depth }); n.children.forEach(walk); };
  nodes.forEach(walk);
  return rows;
}

const active = (s: string) => ['queued', 'running', 'waiting_approval'].includes(s);

/** Collapsed-bar text: agent counts and model assignments (the model each agent runs on, or was asked to run on). */
export function agentsSummary(agents: RuntimeAgent[]): { active: number; models: string } {
  const running = agents.filter(a => active(a.state)).length;
  const tally = new Map<string, number>();
  for (const a of agents) { const m = a.modelActual || a.modelRequested; if (m) tally.set(m, (tally.get(m) ?? 0) + 1); }
  return {
    active: running,
    models: [...tally].map(([m, n]) => n > 1 ? `${m} ×${n}` : m).join(', '),
  };
}

export const agentName = (agents: RuntimeAgent[], id: string) => agents.find(a => a.id === id)?.name || id;

/** The chat message that tells the lead (the only holder of agent.integrate) a conflict was resolved by hand. */
export const resolveMessage = (name: string) => `I resolved the conflict in ${name}; continue integration`;

export function conflictRevisions(integration: AgentIntegrationStatus | undefined, agents: RuntimeAgent[]): { agentId: string; name: string; conflict?: string }[] {
  return (integration?.revisions ?? []).filter(r => r.state === 'conflict').map(r => ({ agentId: r.agentId, name: agentName(agents, r.agentId), conflict: r.conflict }));
}

export interface ReportView { staged: string; unintegrated: string[]; blockedAgents: string[]; blocked?: string; amended: boolean; amendReason: string; message: string; unverified: boolean }
/** What the user must learn after Cancel: applied work stays staged; unintegrated work lives only in agent workspaces. */
export function reportView(r: AgentIntegrationReport, agents: RuntimeAgent[]): ReportView {
  const n = r.appliedCount;
  return {
    staged: n === 0 ? 'No agent work was applied.' : `${n} applied ${n === 1 ? 'change stays' : 'changes stay'} staged and kept.`,
    unintegrated: (r.unintegrated ?? []).map(id => agentName(agents, id)),
    blockedAgents: (r.blockedAgents ?? []).map(id => agentName(agents, id)),
    blocked: r.blocked || undefined,
    amended: !!r.amendments, amendReason: r.amendReason || '',
    message: r.message, unverified: !r.verified,
  };
}

/**
 * Rebuilds the cancel report from the persisted integration status, mirroring the
 * server's grouping (applied, applying = blocked once the task is over, dropped = nowhere,
 * submitted/conflict = unintegrated), so a reload or second client still sees it.
 */
export function statusReport(s: AgentIntegrationStatus): AgentIntegrationReport {
  const applied: string[] = [], unintegrated: string[] = [], blockedAgents: string[] = [];
  for (const r of s.revisions ?? []) {
    if (r.state === 'applied') applied.push(r.agentId);
    else if (r.state === 'applying') blockedAgents.push(r.agentId);
    else if (r.state !== 'dropped') unintegrated.push(r.agentId);
  }
  return { headTree: s.headTree, appliedCount: s.appliedCount, verified: s.verified, applied, unintegrated, blockedAgents, blocked: s.blocked, amendments: s.amendments, amendReason: s.amendReason, message: '' };
}

/** Terminal states that end with a report to show. */
export const reportable = (state: string) => ['cancelled', 'failed', 'interrupted'].includes(state);
