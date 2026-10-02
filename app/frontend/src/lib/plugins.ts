import type { PluginComponentInfo, PluginInfo, PluginInspection, PluginSettingInfo } from './types';

export function convertPluginSettings(schema: PluginSettingInfo[], draft: Record<string, string>): Record<string, unknown> {
	const result: Record<string, unknown> = {};
	for (const setting of schema) {
		if (setting.secret || !(setting.name in draft)) continue;
		const value = draft[setting.name];
		switch ((setting.type || 'string').toLowerCase()) {
		case 'boolean':
		case 'bool':
			if (value !== 'true' && value !== 'false') throw new Error(`${setting.name} must be true or false`);
			result[setting.name] = value === 'true';
			break;
		case 'number': {
			const number = Number(value);
			if (!Number.isFinite(number)) throw new Error(`${setting.name} must be a number`);
			result[setting.name] = number;
			break;
		}
		case 'integer':
		case 'int': {
			const number = Number(value);
			if (!Number.isSafeInteger(number)) throw new Error(`${setting.name} must be an integer`);
			result[setting.name] = number;
			break;
		}
		case 'object':
		case 'array': {
			let parsed: unknown;
			try { parsed = JSON.parse(value); }
			catch { throw new Error(`${setting.name} must contain valid JSON`); }
			if (setting.type === 'object' && (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object')) throw new Error(`${setting.name} must be a JSON object`);
			if (setting.type === 'array' && !Array.isArray(parsed)) throw new Error(`${setting.name} must be a JSON array`);
			result[setting.name] = parsed;
			break;
		}
		default:
			result[setting.name] = value;
		}
	}
	return result;
}

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
