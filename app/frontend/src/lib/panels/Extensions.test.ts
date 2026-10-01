import { describe, expect, it } from 'vitest';
import source from './Extensions.svelte?raw';

describe('Extensions panel', () => {
  it('is a plugin-only inspect-before-install surface', () => {
    expect(source).toContain('Inspect');
    expect(source).toContain('Install');
    expect(source).toContain('managed');
    expect(source).toContain('linked');
    for (const legacyCall of ['skill/list', 'skill/install', 'mcp/list', 'mcp/restart', 'tool/list']) {
      expect(source).not.toContain(legacyCall);
    }
    for (const heading of ['>Skills<', '>MCP servers<', '>Built-in plugins<', '>Tools available to the agent<']) {
      expect(source).not.toContain(heading);
    }
  });
});
