import { expect, it } from 'vitest';
import { approveParams, canApprove, clampLimits, declineParams, defaultModel, draftOf, noEdits, parsePlanResult, parseSuggestionResult, planIdForItem, reviseParams } from './agentPlanCard';

const caps = { files: 'read' as const, shell: 'none' as const, network: false, spawnMax: 0 };
const limits = { maxConcurrent: 4, maxDepth: 2, maxTotal: 10, sliceShare: 0.5, network: false };
const card: any = {
  planId: 'p1', limits,
  agents: [{ name: 'Reviewer', purpose: '', summary: '', capabilities: caps, granted: caps, rejected: null, model: 'a/one' }, { name: 'Writer', purpose: '', summary: '', capabilities: caps, granted: caps, rejected: null }],
  models: [{ name: 'a/one', provider: 'a', toolsOk: true, available: true }, { name: 'b/two', provider: 'b', toolsOk: true, available: true }, { name: 'c/bad', provider: 'c', toolsOk: false, available: true, why: 'no tools' }],
  notices: null, previous: [{ planId: 'p0', name: 'writer', model: 'b/two' }],
};

it('parses tool results', () => {
  expect(parseSuggestionResult('{"suggestionId":"s1","state":"pending","message":"x"}')).toEqual({ suggestionId: 's1', state: 'pending' });
  expect(parseSuggestionResult('nope')).toBeNull();
  expect(parsePlanResult('{"planId":"p","state":"approved"}')).toMatchObject({ planId: 'p', state: 'approved' });
  expect(parsePlanResult('{"state":"revise","feedback":"smaller"}')).toMatchObject({ state: 'revise', feedback: 'smaller' });
});
it('finds the plan from the pending approval card, else the approved result', () => {
  const item: any = { id: 'i', tool: { output: '{"planId":"from-result","state":"approved"}' } };
  expect(planIdForItem(item, [{ itemId: 'i', kind: 'agent_plan', args: { planId: 'from-card' } } as any])).toBe('from-card');
  expect(planIdForItem(item, [])).toBe('from-result');
  expect(planIdForItem({ id: 'i', tool: {} } as any, [])).toBeUndefined();
});
it('previous assignment preselects the model when the entry has none; unusable models never preselect', () => {
  expect(defaultModel(card, card.agents[1])).toBe('b/two');
  expect(defaultModel(card, card.agents[0])).toBe('a/one');
  expect(defaultModel({ ...card, previous: [{ planId: 'p0', name: 'Writer', model: 'c/bad' }] }, card.agents[1])).toBe('');
});
it('limits are bounded by the ceilings', () => {
  expect(clampLimits({ maxConcurrent: 99, maxDepth: 0, maxTotal: 11, sliceShare: 0.9, network: true }, limits)).toEqual({ maxConcurrent: 4, maxDepth: 1, maxTotal: 10, sliceShare: 0.5, network: false });
});
it('approve sends only edited models, dropped names and changed limits', () => {
  const d = draftOf(card, { models: { Reviewer: 'b/two' }, dropped: ['Writer'], limits: { maxConcurrent: 2 } });
  expect(approveParams(card, d)).toEqual({ planId: 'p1', decision: 'approve', models: { Reviewer: 'b/two' }, drop: ['Writer'], limits: { ...limits, maxConcurrent: 2 } });
  expect(approveParams(card, draftOf(card, noEdits()))).toEqual({ planId: 'p1', decision: 'approve', models: { Writer: 'b/two' } });
});
it('approve is impossible when every agent is dropped', () => {
  expect(canApprove(card, draftOf(card, { ...noEdits(), dropped: ['Reviewer', 'Writer'] }))).toBe(false);
  expect(canApprove(card, draftOf(card, noEdits()))).toBe(true);
});
it('revise requires non-empty feedback; decline is bare', () => {
  expect(reviseParams('p1', '  ')).toBeNull();
  expect(reviseParams('p1', ' fewer agents ')).toEqual({ planId: 'p1', decision: 'revise', feedback: 'fewer agents' });
  expect(declineParams('p1')).toEqual({ planId: 'p1', decision: 'decline' });
});
