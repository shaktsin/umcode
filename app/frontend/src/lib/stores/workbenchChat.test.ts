import { afterEach, describe, expect, it, vi } from 'vitest';
import { ThreadView } from './chat.svelte';
import { app } from './app.svelte';
import type { Thread } from '$lib/types';
import type { WorkbenchContext } from '$lib/workbench/assistant';

vi.hoisted(() => { globalThis.window = new EventTarget() as unknown as Window & typeof globalThis; });
afterEach(() => vi.restoreAllMocks());
const context: WorkbenchContext = { projectId: 'p', modelId: 'm', modelName: 'Tiny', graphJson: '{"version":1,"nodes":[],"edges":[]}', revision: 'a'.repeat(64), summary: 'reference' };
function view() {
  const view = new ThreadView(); view.id = 'thread'; view.thread = { id: 'thread', projectId: 'p' } as Thread;
  vi.spyOn(view.delegation, 'bind').mockResolvedValue(); vi.spyOn(view.delegation, 'hydrate').mockResolvedValue();
  return view;
}
describe('workbench chat transport', () => {
  it('submits an explicit snapshot without delegation activation or rewriting the question', async () => {
    const v = view(), call = vi.spyOn(app, 'call').mockResolvedValue({});
    vi.spyOn(v.delegation, 'submission').mockReturnValue({ delegationTaskId: 'old-task' });
    await v.send('Explain this layer', [], undefined, context);
    expect(call).toHaveBeenCalledWith('turn/start', { threadId: 'thread', text: 'Explain this layer', attachments: [], workbench: context });
    expect(v.delegation.bind).not.toHaveBeenCalled(); expect(v.delegation.hydrate).not.toHaveBeenCalled();
  });
  it('leaves ordinary chat submission unchanged', async () => {
    const v = view(), call = vi.spyOn(app, 'call').mockResolvedValue({});
    vi.spyOn(v.delegation, 'submission').mockReturnValue({});
    await v.send('Hello');
    expect(call).toHaveBeenCalledWith('turn/start', { threadId: 'thread', text: 'Hello', attachments: [] });
  });
});
