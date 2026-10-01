import { describe, expect, it } from 'vitest';
import { createPluginClient, groupPluginComponents } from './plugins';
import type { PluginInfo, PluginInspection } from './types';

const plugin: PluginInfo = {
  id: 'source/sample', name: 'Sample', version: '1.0.0', format: 'agent', source: '/sample', mode: 'managed',
  enabled: true, settings: {}, schema: [], diagnostics: [], installedAt: '', updatedAt: '',
  components: [
    { kind: 'skill', name: 'greet', required: false, supported: true },
    { kind: 'mcp', name: 'search', required: true, supported: true },
    { kind: 'hook', name: 'TurnStart', required: false, supported: true },
    { kind: 'agent', name: 'reviewer', required: false, supported: false },
  ],
};

describe('plugin client', () => {
  it('inspects before installing and installs with the inspection token', async () => {
    const calls: Array<{ method: string; params: unknown }> = [];
    const inspection: PluginInspection = { token: 'review-token', expiresAt: '', source: '/sample', mode: 'managed', digest: 'abc', requiresApproval: false, plugin, diagnostics: [] };
    const client = createPluginClient(async (method, params) => {
      calls.push({ method, params });
      return method === 'plugin/inspect' ? inspection : plugin;
    });
    await client.inspectAndInstall('/sample', 'managed', 'project-1');
    expect(calls.map((call) => call.method)).toEqual(['plugin/inspect', 'plugin/install']);
    expect(calls[1].params).toEqual({ token: 'review-token', projectId: 'project-1' });
  });

  it('keeps secrets write-only and uses only plugin routes', async () => {
    const calls: Array<{ method: string; params: unknown }> = [];
    const client = createPluginClient(async (method, params) => {
      calls.push({ method, params });
      if (method === 'plugin/list') return { plugins: [plugin] };
      if (method === 'plugin/inspect') return { token: 't', plugin };
      return plugin;
    });
    await client.inspect('/sample', 'linked');
    await client.install('t', 'project-1');
    await client.list('project-1');
    await client.get(plugin.id, 'project-1');
    await client.setEnabled(plugin.id, 'project-1', false);
    const configured = await client.configure(plugin.id, 'project-1', { region: 'west' }, { api_key: 'secret' });
    await client.reload(plugin.id);
    await client.uninstall(plugin.id, true);
    expect(calls.every((call) => call.method.startsWith('plugin/'))).toBe(true);
    expect(JSON.stringify(configured)).not.toContain('secret');
    expect(groupPluginComponents(plugin)).toEqual({
      skills: [plugin.components[0]], mcpServers: [plugin.components[1]], hooks: [plugin.components[2]], unsupported: [plugin.components[3]],
    });
  });
});
