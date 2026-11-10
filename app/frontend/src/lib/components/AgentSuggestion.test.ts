import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [] } }));
import AgentSuggestion from './AgentSuggestion.svelte';

const item = (output = '{"suggestionId":"s1","state":"pending","message":"Stop now"}') => ({ id: 'i', kind: 'toolCall', tool: { name: 'agent.suggest', args: { reason: 'Three independent modules' }, output } }) as any;
const view = (state: string | null, error = '', queued: string | null = null) => ({
  cards: { suggestion: () => (state ? { id: 's1', state } : undefined), suggestionActionable: () => state === 'pending', error },
  answerSuggestion: vi.fn(), queuedFollowUp: queued, followUpError: '',
}) as any;

it('offers both buttons and the reason only while the engine says pending', () => {
  const { body } = render(AgentSuggestion, { props: { item: item(), view: view('pending') } });
  expect(body).toContain('Three independent modules');
  expect(body).toContain('Enable agents for this message');
  expect(body).toContain('Continue without agents');
});
it.each(['enabled', 'declined', 'expired'])('is a read-only summary when %s', (state) => {
  const { body } = render(AgentSuggestion, { props: { item: item(), view: view(state) } });
  expect(body).toContain('Three independent modules');
  expect(body).not.toContain('Enable agents for this message');
  expect(body).not.toContain('Continue without agents');
});
it('offers nothing before the get RPC has answered, whatever the result text says', () => {
  const { body } = render(AgentSuggestion, { props: { item: item('{"suggestionId":"s1","state":"pending"}'), view: view(null) } });
  expect(body).not.toContain('<button');
});
it('shows a refresh error', () => {
  expect(render(AgentSuggestion, { props: { item: item(), view: view(null, 'boom') } }).body).toContain('boom');
});
it('shows a visible waiting state while the follow-up is queued behind the running turn', () => {
  const body = render(AgentSuggestion, { props: { item: item(), view: view('enabled', '', 'Agents are enabled') } }).body;
  expect(body).toContain('Waiting for the current turn to finish');
  expect(render(AgentSuggestion, { props: { item: item(), view: view('enabled') } }).body).not.toContain('Waiting for the current turn');
});
it('shows the expired-activation message on the card', () => {
  const v = view('enabled'); v.followUpError = 'Activation expired — enable agents again.';
  expect(render(AgentSuggestion, { props: { item: item(), view: v } }).body).toContain('Activation expired');
});
