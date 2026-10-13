import { beforeAll, beforeEach, expect, it, vi } from 'vitest';

vi.mock('./app.svelte', () => ({ app: {
  view: 'chat', rpc: { on() {} }, onConnected() {}, toast() {},
  try: async () => undefined,
  call: async (method: string, params: Record<string, unknown>) => {
    if (method === 'thread/start') return { id: 'new-thread', projectId: params.projectId, updatedAt: '', settings: {} };
    return {};
  },
} }));

let chat: typeof import('./chat.svelte').chat;
let projects: typeof import('./projects.svelte').projects;

beforeAll(async () => {
  vi.stubGlobal('window', { addEventListener() {} });
  ({ chat } = await import('./chat.svelte'));
  ({ projects } = await import('./projects.svelte'));
});

beforeEach(() => {
  projects.activeId = 'previous-project';
  chat.main.reset();
});

it('global new chat sends without the previous project', async () => {
  await chat.newChat();
  await chat.send('Hello');
  expect(projects.activeId).toBeNull();
  expect(chat.main.thread?.projectId).toBeUndefined();
});

it('project-specific new chat sends in the chosen project', async () => {
  await chat.newChatFor('chosen-project');
  await chat.send('Hello');
  expect(chat.main.thread?.projectId).toBe('chosen-project');
});
