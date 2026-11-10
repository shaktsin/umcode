import { expect, it } from 'vitest';
import { buildAgentTree } from './agentTree';
import { statusReport, reportable, agentName, agentsSummary, conflictRevisions, flattenTree, reportView, resolveMessage } from './agentRuntime';
import type { RuntimeAgent } from './types';

const ag = (id: string, parent: string | undefined, extra: Partial<RuntimeAgent> = {}): RuntimeAgent => ({ id, taskId: 't', parentAgentId: parent, name: `n-${id}`, purpose: '', assignment: { prompt: '' }, capabilities: { files: 'read', shell: 'none', network: false, spawnMax: 0 }, budgetShare: 0.1, depth: 0, state: 'running', result: { summary: '' }, usage: { inputTokens: 0, outputTokens: 0, costUsd: 0 }, version: 1, createdAt: `2026-01-01T00:00:0${id.length}Z`, updatedAt: '', ...extra } as RuntimeAgent);

it('flattens the forest in pre-order with depth, preserving agent identity', () => {
  const agents = [ag('a', undefined), ag('bb', 'a'), ag('ccc', 'bb'), ag('dddd', 'ccc')];
  const rows = flattenTree(buildAgentTree(agents));
  expect(rows.map(r => [r.agent.id, r.depth])).toEqual([['a', 0], ['bb', 1], ['ccc', 2], ['dddd', 3]]);
  rows.forEach((r, i) => expect(r.agent).toBe(agents[i]));
});
it('a one-agent update leaves every sibling row object unchanged', () => {
  const before = [ag('a', undefined), ag('bb', 'a'), ag('cc', 'a')];
  const after = before.map(x => x.id === 'bb' ? { ...x, state: 'completed' as const } : x);
  const b = flattenTree(buildAgentTree(before)), a = flattenTree(buildAgentTree(after));
  expect(a.map((r, i) => r.agent === b[i].agent)).toEqual([true, false, true]);
});
it('summarizes counts and model assignments with fallbacks counted on the actual model', () => {
  const s = agentsSummary([ag('a', undefined, { modelRequested: 'p/m1' }), ag('b', undefined, { modelRequested: 'p/m1', modelActual: 'p/m2', state: 'completed' }), ag('c', undefined, { modelRequested: 'p/m1' })]);
  expect(s.active).toBe(2); expect(s.models).toBe('p/m1 ×2, p/m2');
  expect(agentsSummary([ag('a', undefined, { state: 'completed' })]).active).toBe(0);
});
it('builds the resolve message and finds only conflict revisions by agent name', () => {
  expect(resolveMessage('Builder')).toBe('I resolved the conflict in Builder; continue integration');
  const agents = [ag('a', undefined, { name: 'Builder' })];
  const integ = { headTree: 'h', appliedCount: 1, verified: true, revisions: [{ agentId: 'a', state: 'conflict', conflict: 'a.ts' }, { agentId: 'b', state: 'applied' }] };
  expect(conflictRevisions(integ, agents)).toEqual([{ agentId: 'a', name: 'Builder', conflict: 'a.ts' }]);
  expect(conflictRevisions({ ...integ, revisions: [{ agentId: 'a', state: 'applied' }] }, agents)).toEqual([]);
  expect(conflictRevisions(undefined, agents)).toEqual([]); expect(agentName(agents, 'zz')).toBe('zz');
});
it('report says applied work stays staged and names unintegrated and blocked agents', () => {
  const agents = [ag('a', undefined, { name: 'Builder' }), ag('b', undefined, { name: 'Tester' })];
  const v = reportView({ headTree: 'h', appliedCount: 2, verified: false, unintegrated: ['a'], blockedAgents: ['b'], blocked: 'needs approval', amendments: 1, amendReason: 'fix imports', message: 'Cancelled' }, agents);
  expect(v.staged).toBe('2 applied changes stay staged and kept.'); expect(v.unintegrated).toEqual(['Builder']); expect(v.blockedAgents).toEqual(['Tester']);
  expect(v.blocked).toBe('needs approval'); expect(v.amended).toBe(true); expect(v.amendReason).toBe('fix imports'); expect(v.unverified).toBe(true);
  expect(reportView({ headTree: 'h', appliedCount: 1, verified: true, message: '' }, agents)).toMatchObject({ staged: '1 applied change stays staged and kept.', unintegrated: [], amended: false });
});
it('rebuilds the report from persisted status for every revision state', () => {
  const r = statusReport({ headTree: 'h', appliedCount: 1, verified: false, blocked: 'stuck', amendments: 2, amendReason: '', revisions: [
    { agentId: 'ap', state: 'applied' }, { agentId: 'sub', state: 'submitted' }, { agentId: 'con', state: 'conflict', conflict: 'x' }, { agentId: 'ing', state: 'applying' }, { agentId: 'drop', state: 'dropped' }] });
  expect(r.applied).toEqual(['ap']); expect(r.unintegrated).toEqual(['sub', 'con']); expect(r.blockedAgents).toEqual(['ing']);
  expect(r).toMatchObject({ appliedCount: 1, verified: false, blocked: 'stuck', amendments: 2 });
  const v = reportView(r, []); expect(v.staged).toBe('1 applied change stays staged and kept.'); expect(v.amended).toBe(true); expect(v.amendReason).toBe('');
  expect(statusReport({ headTree: 'h', appliedCount: 0, verified: true })).toMatchObject({ applied: [], unintegrated: [], blockedAgents: [] });
  expect(['cancelled', 'failed', 'interrupted', 'running', 'completed'].map(reportable)).toEqual([true, true, true, false, false]);
});
