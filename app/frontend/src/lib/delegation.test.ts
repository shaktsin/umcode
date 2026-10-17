import { beforeAll, beforeEach, expect, it, vi } from 'vitest';
const { call, handlers } = vi.hoisted(() => ({ call: vi.fn(), handlers: new Map<string, (p: any) => void>() }));
vi.mock('./stores/app.svelte', () => ({ app: { call, try: call, toast() {}, onConnected() {}, rpc: { on(n: string, f: (p: any) => void) { handlers.set(n, f); } } } }));
let DelegationState: typeof import('./stores/delegation.svelte').DelegationState;
let ThreadView: typeof import('./stores/chat.svelte').ThreadView;
beforeAll(async () => { vi.stubGlobal('window', { addEventListener() {} }); ({ DelegationState } = await import('./stores/delegation.svelte')); ({ ThreadView } = await import('./stores/chat.svelte')); });
beforeEach(() => { call.mockReset(); call.mockResolvedValue({ tasks: [] }); });
const child = (version = 1, summary = '') => ({ id: 'child', taskId: 'task', role: 'implementer' as const, state: 'running' as const, version, assignment: { prompt: 'Fix it' }, result: { summary }, usage: { inputTokens: 10, outputTokens: 2, costUsd: .01, requests: 1, cachedInputTokens: 0, reasoningTokens: 0 }, correctionRounds: 0, createdAt: '', updatedAt: '' });
const task = { id: 'task', threadId: 'parent', state: 'running' as const, updatedAt: '2026-10-15T12:00:00Z', createdAt: '', rootTurnId: 'root', projectId: 'project', activation: 'explicit_user', baseRevision: 'abc' };
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => resolve = r); return { promise, resolve }; }
it('upserts versions and ordered same-version finalization errors and recovery without fake child rows', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project');
  s.apply({ threadId: 'parent', taskId: 'task', task, child: child(2), sequence: 10 });
  s.apply({ threadId: 'parent', taskId: 'task', child: child(1), sequence: 11 });
  s.apply({ threadId: 'parent', taskId: 'task', child: child(2, 'partial'), error: 'storage unavailable', sequence: 12 });
  s.apply({ threadId: 'parent', taskId: 'task', child: child(2), sequence: 10 });
  expect(s.tasks[0].children).toHaveLength(1); expect(s.tasks[0].children[0].result.summary).toBe('partial'); expect(s.tasks[0].childErrors.child).toBe('storage unavailable');
  s.apply({ threadId: 'parent', taskId: 'task', child: child(2, 'saved'), sequence: 13 });
  s.apply({ threadId: 'parent', taskId: 'task', task: { ...task, state: 'completed', updatedAt: '2026-10-15T12:01:00Z' }, workflow: { taskId: 'task', childId: 'child', revision: 'abc', checks: [{ label: 'test', command: 'test', status: 'blocked' }] } });
  expect(s.tasks[0].childErrors.child).toBeUndefined(); expect(s.tasks[0].children).toHaveLength(1); expect(s.tasks[0].workflow).toBeUndefined();
});
it('ignores stale chat responses and live events cannot be overwritten by a reconnect fetch', async () => {
  const s = new DelegationState(); const old = deferred<any>(); call.mockReturnValueOnce(old.promise); const loading = s.bind('old', 'project'); await s.bind('parent', 'project'); old.resolve({ tasks: [{ task: { ...task, threadId: 'old' }, children: [child()] }] }); await loading; expect(s.tasks).toEqual([]);
  call.mockResolvedValueOnce({ tasks: [{ task, children: [child(2)], childErrors: { child: 'retained' }, childSequences: { child: 12 } }] }); await s.hydrate(); expect(s.tasks[0].childErrors.child).toBe('retained');
  const reload = deferred<any>(); call.mockReturnValueOnce(reload.promise); const reloading = s.hydrate(); s.apply({ threadId: 'parent', taskId: 'task', child: child(2, 'recovered'), sequence: 13 }); reload.resolve({ tasks: [{ task, children: [child(2)], childErrors: { child: 'retained' }, childSequences: { child: 12 } }] }); await reloading; expect(s.tasks[0].children[0].result.summary).toBe('recovered'); expect(s.tasks[0].childErrors.child).toBeUndefined();
});
it('manual activation is one-use, expires actionably and resets on chat or project changes', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project'); call.mockResolvedValueOnce({ activationToken: 'token', expiresAt: new Date(Date.now() + 10000).toISOString() }); await s.enable(); expect(s.submission()).toEqual({ delegationActivationToken: 'token' }); expect(s.submission()).toEqual({});
  call.mockResolvedValueOnce({ activationToken: 'expired', expiresAt: '2000-01-01' }); await s.enable(); expect(() => s.submission()).toThrow(/expired/);
  await s.bind('parent', 'different'); expect(s.token).toBeNull();
});
it('parent stop cancels admitted task and also interrupts its running ordinary turn', async () => {
  const v = new ThreadView(); v.id = 'parent'; await v.delegation.bind('parent', 'project'); v.delegation.apply({ threadId: 'parent', taskId: 'task', task, child: child() });
  v.turns = [{ id: 'root', threadId: 'parent', status: 'running' } as any]; call.mockImplementation(async (method: string) => method === 'delegation/cancel' ? { task: { ...task, state: 'cancelled' }, children: [child()] } : {});
  await v.interrupt(); expect(call).toHaveBeenCalledWith('delegation/cancel', { threadId: 'parent', taskId: 'task' }); expect(call).toHaveBeenCalledWith('turn/interrupt', { turnId: 'root' }); expect(v.delegation.tasks[0].task.state).toBe('cancelled');
});
it('unfinished task follows up by durable task ID and independent chat instances never share permission', async () => {
  const a = new ThreadView(), b = new ThreadView(); a.id = 'parent'; a.thread = { id: 'parent', projectId: 'project' } as any; b.id = 'other'; b.thread = { id: 'other', projectId: 'project' } as any; await a.delegation.bind('parent', 'project'); await b.delegation.bind('other', 'project'); a.delegation.apply({ threadId: 'parent', taskId: 'task', task }); await a.send('continue'); expect(call).toHaveBeenCalledWith('turn/start', expect.objectContaining({ delegationTaskId: 'task' })); expect(b.delegation.submission()).toEqual({});
});
it('late workflow deliveries hydrate current durable evidence instead of regressing checks', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project');
  call.mockResolvedValueOnce({ tasks: [{ task, children: [], workflow: { taskId: 'task', childId: 'child', revision: 'abc', checks: [{ label: 'Lead tests', command: 'test', status: 'passed' }] } }] }); await s.hydrate();
  call.mockResolvedValueOnce({ tasks: [{ task, children: [], workflow: { taskId: 'task', childId: 'child', revision: 'abc', checks: [{ label: 'Lead tests', command: 'test', status: 'passed' }] } }] });
  s.apply({ threadId: 'parent', taskId: 'task', task, workflow: { taskId: 'task', childId: 'child', revision: 'abc', checks: [{ label: 'Lead tests', command: 'test', status: 'not_run' }] } });
  expect(s.tasks[0].workflow?.checks[0].status).toBe('passed');
});
it('restart hydration clears transient same-version errors and rejects delayed old notifications', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project'); s.apply({ threadId: 'parent', taskId: 'task', task, child: child(2, 'pending'), error: 'old process error', sequence: 12 });
  call.mockResolvedValueOnce({ tasks: [{ task, children: [child(2, 'durably saved')], childSequences: { child: 0 } }] }); await s.hydrate();
  expect(s.tasks[0].childErrors.child).toBeUndefined(); expect(s.tasks[0].children[0].result.summary).toBe('durably saved');
  s.apply({ threadId: 'parent', taskId: 'task', child: child(2, 'pending'), error: 'old process error', sequence: 12 });
  expect(s.tasks[0].children[0].result.summary).toBe('durably saved'); expect(s.tasks[0].childErrors.child).toBeUndefined();
});
it('new project chat activates before its first message and repeated clicks create one empty thread', async () => {
  const { chat } = await import('./stores/chat.svelte'); const { projects } = await import('./stores/projects.svelte');
  await chat.newChatFor('project'); const opening = deferred<any>();
  call.mockImplementation(async (method: string) => {
    if (method === 'thread/start') return opening.promise;
    if (method === 'delegation/enable') return { activationToken: 'first-message', expiresAt: new Date(Date.now() + 10000).toISOString() };
    return { tasks: [] };
  });
  const first = chat.enableDelegation(chat.main), repeat = chat.enableDelegation(chat.main);
  opening.resolve({ id: 'parent', projectId: 'project', settings: {}, updatedAt: '' }); await Promise.all([first, repeat]);
  expect(call.mock.calls.filter(([m]) => m === 'thread/start')).toHaveLength(1);
  expect(call).toHaveBeenCalledWith('thread/start', expect.objectContaining({ projectId: 'project' }));
  await chat.send('build it'); expect(call).toHaveBeenCalledWith('turn/start', expect.objectContaining({ threadId: 'parent', delegationActivationToken: 'first-message' }));
  expect(projects.activeId).toBe('project');
});
it('projectless activation explains unsupported scope without creating a chat or granting permission', async () => {
  const { chat } = await import('./stores/chat.svelte'); await chat.newChatFor(null); await chat.enableDelegation(chat.main);
  expect(chat.main.delegation.error).toMatch(/Git project/i); expect(chat.main.id).toBeNull(); expect(call.mock.calls.some(([m]) => m === 'delegation/enable' || m === 'thread/start')).toBe(false);
});
it('project switch while creating an activation chat never attaches its token to the new scope', async () => {
  const { chat } = await import('./stores/chat.svelte'); await chat.newChatFor('old-project'); const opening = deferred<any>();
  call.mockImplementation(async (method: string) => method === 'thread/start' ? opening.promise : { tasks: [] });
  const enabling = chat.enableDelegation(chat.main); await chat.newChatFor('new-project'); opening.resolve({ id: 'old-chat', projectId: 'old-project', settings: {}, updatedAt: '' }); await enabling;
  expect(chat.main.id).toBeNull(); expect(chat.main.delegation.token).toBeNull(); expect(call.mock.calls.some(([m]) => m === 'delegation/enable')).toBe(false);
});
it('switching chats revokes a prepared token before thread/read resolves', async () => {
  const v = new ThreadView(); v.id = 'parent'; v.thread = { id: 'parent', projectId: 'project' } as any; await v.delegation.bind('parent', 'project');
  call.mockResolvedValueOnce({ activationToken: 'old', expiresAt: new Date(Date.now() + 10000).toISOString() }); await v.delegation.enable();
  const reading = deferred<any>(); call.mockImplementation(async (method: string) => method === 'thread/read' ? reading.promise : { tasks: [] });
  const loading = v.load('other'); expect(v.delegation.token).toBeNull(); expect(v.delegation.threadId).toBe('other');
  reading.resolve({ thread: { id: 'other', projectId: 'project', settings: {} }, turns: [], items: [] }); await loading;
});
it('submission waits while explicit activation is pending', async () => {
  const v = new ThreadView(); v.id = 'parent'; v.thread = { id: 'parent', projectId: 'project' } as any; await v.delegation.bind('parent', 'project'); v.delegation.pending = true;
  await v.send('first message'); expect(call.mock.calls.some(([m]) => m === 'turn/start')).toBe(false);
});
it('child events refresh shared actual usage', async () => {
  const v = new ThreadView(); await v.delegation.bind('parent', 'project');
  v.delegation.apply({ threadId: 'parent', taskId: 'task', task });
  call.mockResolvedValueOnce({ tasks: [{ task, children: [child(2)], budgetAvailable: true, usage: { ...child().usage, costUsd: .5 } }] });
  v.delegation.apply({ threadId: 'parent', taskId: 'task', child: child(2), sequence: 2 });
  await vi.waitFor(() => expect(v.delegation.tasks[0].usage?.costUsd).toBe(.5));
});
it('new-chat activation remains pending across delayed hydration until its token is ready', async () => {
  const { chat } = await import('./stores/chat.svelte'); await chat.newChatFor('project'); const list = deferred<any>(); const permission = deferred<any>();
  call.mockImplementation(async (method: string) => {
    if (method === 'thread/start') return { id: 'parent', projectId: 'project', settings: {}, updatedAt: '' };
    if (method === 'delegation/list') return list.promise;
    if (method === 'delegation/enable') return permission.promise;
    return {};
  });
  const enabling = chat.enableDelegation(chat.main); await vi.waitFor(() => expect(chat.main.id).toBe('parent'));
  expect(chat.main.delegation.pending).toBe(true); await chat.send('should wait'); expect(call.mock.calls.some(([m]) => m === 'turn/start')).toBe(false);
  await chat.enableDelegation(chat.main); expect(call.mock.calls.filter(([m]) => m === 'thread/start')).toHaveLength(1); expect(call.mock.calls.some(([m]) => m === 'delegation/enable')).toBe(false);
  list.resolve({ tasks: [] }); await vi.waitFor(() => expect(call.mock.calls.some(([m]) => m === 'delegation/enable')).toBe(true)); expect(chat.main.delegation.pending).toBe(true);
  permission.resolve({ activationToken: 'ready', expiresAt: new Date(Date.now() + 10000).toISOString() }); await enabling;
  await chat.send('first paid message'); expect(call).toHaveBeenCalledWith('turn/start', expect.objectContaining({ delegationActivationToken: 'ready' }));
});
it('navigation while activation hydration waits cannot enable the replacement chat', async () => {
  const { chat } = await import('./stores/chat.svelte'); await chat.newChatFor('project'); const list = deferred<any>();
  call.mockImplementation(async (method: string, params: any) => {
    if (method === 'thread/start') return { id: 'parent', projectId: 'project', settings: {}, updatedAt: '' };
    if (method === 'delegation/list' && params.threadId === 'parent') return list.promise;
    if (method === 'thread/read') return { thread: { id: 'replacement', projectId: 'project', settings: {} }, turns: [], items: [] };
    return { tasks: [] };
  });
  const enabling = chat.enableDelegation(chat.main); await vi.waitFor(() => expect(chat.main.id).toBe('parent')); await chat.open('replacement'); list.resolve({ tasks: [] }); await enabling;
  expect(chat.main.id).toBe('replacement'); expect(chat.main.delegation.token).toBeNull(); expect(call.mock.calls.some(([m]) => m === 'delegation/enable')).toBe(false);
});
it('orders RFC3339 task updates by instant including fractional seconds and nanoseconds', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project');
  const update = (updatedAt: string, state: 'running' | 'cancelled') => s.apply({ threadId: 'parent', taskId: 'task', task: { ...task, updatedAt, state } });
  update('2026-10-15T12:00:05Z', 'cancelled');
  update('2026-10-15T12:00:05.5Z', 'running');
  expect(s.active?.task.state).toBe('running');
  update('2026-10-15T12:00:05Z', 'cancelled');
  expect(s.active?.task.state).toBe('running');
  update('2026-10-15T12:00:06.000001Z', 'running');
  update('2026-10-15T12:00:06.000002Z', 'cancelled');
  update('2026-10-15T12:00:06.000001Z', 'running');
  expect(s.active).toBeUndefined();
  expect(s.tasks[0].task.updatedAt).toBe('2026-10-15T12:00:06.000002Z');
  update('2026-10-15T05:00:06.000002001-07:00', 'running');
  expect(s.active?.task.state).toBe('running');
  update('2026-10-15T12:00:06.000002Z', 'cancelled');
  expect(s.active?.task.state).toBe('running');
});
it('invalid task timestamps cannot introduce activation or regress retained state', async () => {
  const s = new DelegationState(); await s.bind('parent', 'project');
  s.apply({ threadId: 'parent', taskId: 'task', task: { ...task, updatedAt: 'invalid' } });
  expect(s.tasks).toEqual([]); expect(s.active).toBeUndefined();
  s.apply({ threadId: 'parent', taskId: 'task', task: { ...task, state: 'cancelled' } });
  for (const updatedAt of ['invalid', '2099-02-30T12:00:00Z', '2099-01-01T25:00:00Z', '2099-01-01T12:00:00+24:00']) {
    s.apply({ threadId: 'parent', taskId: 'task', task: { ...task, updatedAt } });
    expect(s.active).toBeUndefined(); expect(s.tasks[0].task.state).toBe('cancelled');
  }
});
it('hydration skips malformed task timestamps while retaining valid snapshots', async () => {
  const s = new DelegationState();
  call.mockResolvedValueOnce({ tasks: [
    { task: { ...task, id: 'invalid', updatedAt: 'invalid' }, children: [] },
    { task: { ...task, state: 'cancelled' }, children: [] },
  ] });
  await s.bind('parent', 'project');
  expect(s.tasks.map(snapshot => snapshot.task.id)).toEqual(['task']);
  expect(s.active).toBeUndefined(); expect(s.error).toBe('');
});
