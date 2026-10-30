import { describe, expect, it } from 'vitest';
import { formatTurnTime, progressPhrase } from './turnProgress';
import type { Item } from './types';

function item(kind: Item['kind'], status: Item['status'], name = '', seq = 1): Item {
  return {
    id: `item-${seq}`, threadId: 'thread-1', turnId: 'turn-1', seq,
    kind, status, createdAt: '2026-10-30T20:00:00Z',
    ...(name ? { tool: { callId: `call-${seq}`, name, args: {} } } : {}),
  };
}

describe('live turn progress', () => {
  it('shows an observed action, then updates when that action finishes', () => {
    const reading = item('toolCall', 'inProgress', 'file.read');
    expect(progressPhrase([reading], false)).toBe('Reading project files');
    expect(progressPhrase([{ ...reading, status: 'completed' }], false)).toBe('Finished reading project files');
    expect(progressPhrase([reading, item('toolCall', 'inProgress', 'verification.run', 2)], false)).toBe('Running checks');
  });

  it('reports approval and response writing without claiming an unobserved task', () => {
    expect(progressPhrase([], false)).toBe('Starting request');
    expect(progressPhrase([item('agentMessage', 'inProgress')], false)).toBe('Writing a response');
    expect(progressPhrase([item('toolCall', 'inProgress', 'shell.run')], true)).toBe('Waiting for your approval');
  });

  it('keeps the running time legible past a minute and ignores bad clocks', () => {
    const start = '2026-10-30T20:00:00Z';
    expect(formatTurnTime(start, Date.parse(start) + 13_000)).toBe('13s');
    expect(formatTurnTime(start, Date.parse(start) + 62_000)).toBe('1m 02s');
    expect(formatTurnTime(start, Date.parse(start) + 3_661_000)).toBe('1h 01m 01s');
    expect(formatTurnTime(start, Date.parse(start) - 1_000)).toBe('0s');
    expect(formatTurnTime('not-a-date', Date.parse(start))).toBe('0s');
  });
});
