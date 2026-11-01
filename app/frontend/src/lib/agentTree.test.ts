import { expect, it } from 'vitest';
import { agentFellBack, buildAgentTree, capabilityChips } from './agentTree';
import type { RuntimeAgent } from './types';

const caps = { files: 'read' as const, shell: 'none' as const, network: false, spawnMax: 0 };
const ag = (id: string, parent: string | undefined, created: string, extra: Partial<RuntimeAgent> = {}): RuntimeAgent => ({ id, taskId: 't', parentAgentId: parent, name: id, purpose: '', assignment: { prompt: '' }, capabilities: caps, budgetShare: 0, depth: 0, state: 'running', result: { summary: '' }, usage: {} as any, version: 1, createdAt: created, updatedAt: created, ...extra });

it('builds parent/child nesting to depth 3 ordered by createdAt', () => {
  const t = buildAgentTree([ag('c', 'b', '2026-01-01T00:00:03Z'), ag('b2', 'a', '2026-01-01T00:00:05Z'), ag('a', undefined, '2026-01-01T00:00:01Z'), ag('b', 'a', '2026-01-01T00:00:02Z'), ag('d', 'c', '2026-01-01T00:00:04Z')]);
  expect(t.map(n => n.agent.id)).toEqual(['a']);
  expect(t[0].children.map(n => n.agent.id)).toEqual(['b', 'b2']);
  const d = t[0].children[0].children[0].children[0];
  expect([d.agent.id, d.depth]).toEqual(['d', 3]);
});
it('puts orphans and cycles at the root and keeps input order for equal timestamps', () => {
  const t = buildAgentTree([ag('x', 'missing', '2026-01-01T00:00:01Z'), ag('r', undefined, '2026-01-01T00:00:01Z'), ag('p', 'q', '2026-01-01T00:00:02Z'), ag('q', 'p', '2026-01-01T00:00:02Z')]);
  expect(t.map(n => n.agent.id)).toEqual(['x', 'r', 'q']);
  expect(t[2].children.map(n => n.agent.id)).toEqual(['p']);
});
it('marks files:rw, shell:rw, network and spawn prominent; shell none is absent', () => {
  expect(capabilityChips({ files: 'read-write', shell: 'read-write', network: true, spawnMax: 2 })).toEqual([
    { label: 'files:rw', prominent: true }, { label: 'shell:rw', prominent: true }, { label: 'network', prominent: true }, { label: 'spawn', prominent: true }]);
  const quiet = capabilityChips({ files: 'read', shell: 'none', network: false, spawnMax: 0 });
  expect(quiet).toEqual([{ label: 'files:r', prominent: false }]);
  expect(quiet.some(c => c.label.startsWith('shell'))).toBe(false);
  expect(capabilityChips({ ...caps, shell: 'read-only' })).toContainEqual({ label: 'shell:ro', prominent: false });
});
it('agentFellBack returns the actual model only when it differs', () => {
  expect(agentFellBack(ag('a', undefined, '', { modelRequested: 'p/m1', modelActual: 'p/m2' }))).toBe('p/m2');
  expect(agentFellBack(ag('a', undefined, '', { modelRequested: 'p/m1', modelActual: 'p/m1' }))).toBeNull();
  expect(agentFellBack(ag('a', undefined, '', { modelRequested: 'p/m1' }))).toBeNull();
});
