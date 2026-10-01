import type { PluginComponentInfo, PluginInfo, PluginInspection } from './types';

export type PluginCall = (method: string, params?: unknown) => Promise<unknown>;

export function createPluginClient(call: PluginCall) {
  const invoke = async <T>(method: string, params?: unknown): Promise<T> => call(method, params) as Promise<T>;
  return {
    inspect: (source: string, mode: 'managed' | 'linked' = 'managed') =>
      invoke<PluginInspection>('plugin/inspect', { source, mode }),
    install: (token: string, projectId?: string) =>
      invoke<PluginInfo>('plugin/install', { token, projectId }),
    list: async (projectId?: string) =>
      (await invoke<{ plugins: PluginInfo[] }>('plugin/list', { projectId })).plugins ?? [],
    get: (pluginId: string, projectId?: string) =>
      invoke<PluginInfo>('plugin/get', { pluginId, projectId }),
    setEnabled: (pluginId: string, projectId: string, enabled: boolean) =>
      invoke<PluginInfo>('plugin/setEnabled', { pluginId, projectId, enabled }),
    configure: (pluginId: string, projectId: string, settings: Record<string, unknown>, secrets: Record<string, string>) =>
      invoke<PluginInfo>('plugin/configure', { pluginId, projectId, settings, secrets }),
    reload: (pluginId: string) => invoke<PluginInfo>('plugin/reload', { pluginId }),
    uninstall: (pluginId: string, disableProjects = false) =>
      invoke<{ ok: boolean }>('plugin/uninstall', { pluginId, disableProjects }),
    async inspectAndInstall(source: string, mode: 'managed' | 'linked', projectId?: string) {
      const inspection = await this.inspect(source, mode);
      return this.install(inspection.token, projectId);
    },
  };
}

export function groupPluginComponents(plugin: PluginInfo): {
  skills: PluginComponentInfo[];
  mcpServers: PluginComponentInfo[];
  hooks: PluginComponentInfo[];
  unsupported: PluginComponentInfo[];
} {
  return {
    skills: plugin.components.filter((component) => component.supported && component.kind === 'skill'),
    mcpServers: plugin.components.filter((component) => component.supported && component.kind === 'mcp'),
    hooks: plugin.components.filter((component) => component.supported && component.kind === 'hook'),
    unsupported: plugin.components.filter((component) => !component.supported),
  };
}
