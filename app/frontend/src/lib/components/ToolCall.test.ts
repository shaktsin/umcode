import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import type { Item } from '$lib/types';
import { agentToolFailure, isAgentTool, taskFailurePresentation } from '$lib/taskOutcome';
import ToolCall from './ToolCall.svelte';
import ChatError from './ChatError.svelte';

const short = 'Agents are not enabled for this message. Agent tools work only after the user approves an agent plan.';
const failure = { category: 'policy_denied', operationId: 'op1', message: short, retryable: false, nextStep: 'Continue without agents in this message; do not retry agent tools until the user has approved an agent plan.' };
const toolItem = (name: string, status: Item['status'], f: unknown = failure): Item => ({
  id: 'it1', kind: 'toolCall', threadId: 'th', turnId: 'turn', seq: 1, status, createdAt: '',
  tool: { callId: 'c1', name, args: { agents: [] }, error: short }, data: { actionFailure: f },
} as Item);

describe('failed agent tool calls', () => {
  it('recognises agent tools by registered and wire name only', () => {
    expect(isAgentTool('agent.propose_plan')).toBe(true);
    expect(isAgentTool('agent__spawn')).toBe(true);
    expect(isAgentTool('agentic.run')).toBe(false);
    expect(isAgentTool('shell.run')).toBe(false);
  });
  it('gives the short message and the failure record for refused agent calls only', () => {
    expect(agentToolFailure(toolItem('agent__propose_plan', 'denied'))).toEqual({ message: short, details: JSON.stringify(failure, null, 2) });
    expect(agentToolFailure(toolItem('agent.spawn', 'failed', null))?.message).toBe(short);
    expect(agentToolFailure(toolItem('agent.spawn', 'completed'))).toBeNull();
    expect(agentToolFailure(toolItem('shell.run', 'failed'))).toBeNull();
  });
  it('renders the short message with technical details collapsed', () => {
    const { body } = render(ToolCall, { props: { item: toolItem('agent__propose_plan', 'denied') } });
    expect(body).toContain(short);
    expect(body).toContain('Technical details');
    expect(body).not.toMatch(/<details[^>]*\sopen/);
    expect(body).not.toContain('truncate">' + short);
  });
  it('keeps other failed tools as before', () => {
    const { body } = render(ToolCall, { props: { item: toolItem('shell.run', 'failed') } });
    expect(body).not.toContain('Technical details');
    expect(body).toContain(short);
  });
  it('shows a non-advisory agent refusal in the chat alert by its message, details collapsed', () => {
    const rule = { ...failure, message: 'agent.propose_plan is lead-only: agents and child tasks cannot propose plans', nextStep: 'Only the lead can do this; report back to the lead instead of retrying.' };
    const p = taskFailurePresentation([toolItem('agent.propose_plan', 'denied', rule)], '', 'completed');
    expect(p.text).toBe(rule.message);
    expect(p.details).toBe(JSON.stringify(rule, null, 2));
    const { body } = render(ChatError, { props: { text: p.text, details: p.details } });
    expect(body).toContain(rule.message);
    expect(body).not.toMatch(/<details[^>]*\sopen/);
  });
});
