import { describe, it, expect } from 'vitest';
import { formatChatError } from './chatError';

const raw = 'openai (ChatGPT sign-in): HTTP 429: {"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1791047417,"resets_in_seconds":74488}} (no other model was available)';

describe('chat error presentation', () => {
  it('extracts usage limits and their absolute reset time from wrapped provider errors', () => {
    const result = formatChatError(raw);
    expect(result.message).toContain('ChatGPT usage limit reached');
    expect(result.message).not.toContain('HTTP');
    expect(result.message).not.toContain('{');
    expect(result.resetAt).toBe(1791047417000);
  });
  it('extracts other provider messages without inventing a reset time', () => {
    expect(formatChatError('openai: HTTP 500: {"error":{"message":"Service unavailable"}}').message).toBe('Service unavailable');
    expect(formatChatError('openai: HTTP 500: {"error":{"message":"Service unavailable"}}').resetAt).toBeUndefined();
  });
  it('preserves malformed and plain errors', () => {
    expect(formatChatError('Connection closed').message).toBe('Connection closed');
    expect(formatChatError('HTTP 429: {broken}').message).toBe('HTTP 429: {broken}');
  });
  it('does not use relative retry seconds for old persisted errors', () => {
    expect(formatChatError('HTTP 429: {"error":{"type":"usage_limit_reached","resets_in_seconds":10,"resets_at":"bad"}}').resetAt).toBeUndefined();
  });
});
