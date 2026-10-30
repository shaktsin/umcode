import type { Item } from './types';

const toolPhrases: Record<string, string> = {
  'file.read': 'Reading project files',
  'file.list': 'Listing project files',
  'file.search': 'Searching project files',
  'file.write': 'Writing project files',
  'file.edit': 'Editing project files',
  'shell.run': 'Running a command',
  'exec.start': 'Starting a command',
  'exec.write': 'Working in the terminal',
  'exec.stop': 'Stopping a command',
  'verification.plan': 'Planning checks',
  'verification.run': 'Running checks',
  'browser.verify': 'Checking the browser',
  'web.search': 'Searching the web',
  'web.fetch': 'Reading a web page',
  'preview.start': 'Starting a preview',
  'preview.stop': 'Stopping a preview',
  'computer.list': 'Checking available apps',
  'computer.start': 'Opening an app',
  'computer.inspect': 'Inspecting an app',
  'computer.act': 'Using an app',
  'computer.stop': 'Closing an app session',
  'visual.start': 'Starting visual inspection',
  'visual.inspect': 'Inspecting the screen',
  'visual.act': 'Using the screen',
  'visual.stop': 'Finishing visual inspection',
  'tools.discover': 'Finding available tools',
  'task.context': 'Reading task context',
  'task.propose': 'Preparing a task proposal',
  'work.update': 'Updating work progress',
};

function toolPhrase(item: Item): string {
  const name = item.tool?.name ?? '';
  if (toolPhrases[name]) return toolPhrases[name];
  if (name.startsWith('agent.')) return 'Working with an agent';
  return name ? `Using ${name}` : 'Working on an action';
}

/** Describe only activity the client has actually received. */
export function progressPhrase(items: Item[], approvalPending: boolean): string {
  if (approvalPending) return 'Waiting for your approval';
  const active = items.filter((item) => item.status === 'inProgress' && (item.kind === 'toolCall' || item.kind === 'agentMessage'))
    .sort((a, b) => b.seq - a.seq)[0];
  if (active?.kind === 'agentMessage') return 'Writing a response';
  if (active) return toolPhrase(active);

  const lastTool = items.filter((item) => item.kind === 'toolCall').sort((a, b) => b.seq - a.seq)[0];
  if (!lastTool) return 'Starting request';
  if (lastTool.status === 'failed' || lastTool.status === 'denied') return 'Action stopped; continuing';
  const phrase = toolPhrase(lastTool);
  return `Finished ${phrase.charAt(0).toLowerCase()}${phrase.slice(1)}`;
}

/** Format a turn clock from server timestamps so a remounted chat resumes accurately. */
export function formatTurnTime(startedAt: string, nowMs: number): string {
  const startedMs = Date.parse(startedAt);
  const seconds = Number.isFinite(startedMs) && Number.isFinite(nowMs)
    ? Math.max(0, Math.floor((nowMs - startedMs) / 1000)) : 0;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  if (hours) return `${hours}h ${String(minutes).padStart(2, '0')}m ${String(remainder).padStart(2, '0')}s`;
  if (minutes) return `${minutes}m ${String(remainder).padStart(2, '0')}s`;
  return `${seconds}s`;
}
