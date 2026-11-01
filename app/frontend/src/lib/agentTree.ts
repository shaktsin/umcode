import type { AgentCapabilities, RuntimeAgent } from './types';

export interface AgentNode { agent: RuntimeAgent; children: AgentNode[]; depth: number }

/** Builds the agent forest: ordered by createdAt (input order breaks ties), orphans and cycles attach under the root. */
export function buildAgentTree(agents: RuntimeAgent[]): AgentNode[] {
  const order = new Map(agents.map((a, i) => [a.id, i]));
  const sorted = [...agents].sort((a, b) => (Date.parse(a.createdAt) || 0) - (Date.parse(b.createdAt) || 0) || order.get(a.id)! - order.get(b.id)!);
  const nodes = new Map<string, AgentNode>(sorted.map(a => [a.id, { agent: a, children: [], depth: 0 }]));
  const roots: AgentNode[] = [];
  const attached = new Map<string, string>(); // child id -> parent id actually used
  const reaches = (from: string, target: string) => { // would parenting `target` under `from` close a cycle
    for (let id: string | undefined = from; id; id = attached.get(id)) if (id === target) return true;
    return false;
  };
  for (const a of sorted) {
    const node = nodes.get(a.id)!, parent = a.parentAgentId ? nodes.get(a.parentAgentId) : undefined;
    if (parent && !reaches(parent.agent.id, a.id)) { parent.children.push(node); attached.set(a.id, parent.agent.id); } else roots.push(node);
  }
  const setDepth = (n: AgentNode, d: number) => { n.depth = d; n.children.forEach(c => setDepth(c, d + 1)); };
  roots.forEach(r => setDepth(r, 0));
  return roots;
}

/** The model an agent actually ran on, only when it differs from the requested one. */
export function agentFellBack(a: RuntimeAgent): string | null {
  return a.modelActual && a.modelRequested && a.modelActual !== a.modelRequested ? a.modelActual : null;
}

export function capabilityChips(c: AgentCapabilities): { label: string; prominent: boolean }[] {
  const chips: { label: string; prominent: boolean }[] = [];
  if (c.files === 'read-write') chips.push({ label: 'files:rw', prominent: true });
  else if (c.files === 'read') chips.push({ label: 'files:r', prominent: false });
  if (c.shell === 'read-write') chips.push({ label: 'shell:rw', prominent: true });
  else if (c.shell === 'read-only') chips.push({ label: 'shell:ro', prominent: false });
  if (c.network) chips.push({ label: 'network', prominent: true });
  if (c.spawnMax > 0) chips.push({ label: 'spawn', prominent: true });
  return chips;
}
