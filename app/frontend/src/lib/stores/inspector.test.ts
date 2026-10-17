import { beforeAll, beforeEach, expect, it, vi } from 'vitest';
import type { Item, ProjectFileContent, BrowserArtifact } from '$lib/types';
const { handlers, request } = vi.hoisted(() => ({ handlers: new Map<string, (payload: any) => void>(), request: vi.fn() }));
vi.mock('./app.svelte', () => ({ app: {
  rpc: { on(name: string, callback: (payload: any) => void) { handlers.set(name, callback); } },
  onConnected() {}, toast() {}, try: request, call: async () => ({}),
} }));
let inspector: typeof import('./inspector.svelte').inspector;
let chat: typeof import('./chat.svelte').chat;
let projects: typeof import('./projects.svelte').projects;
beforeAll(async () => {
  vi.stubGlobal('window', { addEventListener() {} });
  ({ inspector } = await import('./inspector.svelte'));({ chat } = await import('./chat.svelte'));({ projects } = await import('./projects.svelte'));
});
beforeEach(() => {
  inspector.closeAll();request.mockReset();projects.activeId='project-a';chat.main.reset();chat.sides=[];
  chat.main.id='thread-a';chat.main.thread={id:'thread-a',projectId:'project-a',title:'A',workspaceMode:'local',channel:'main',pinned:false,archived:false,usage:{inputTokens:0,outputTokens:0,cachedInputTokens:0,reasoningTokens:0,costUsd:0,requests:0},settings:{},createdAt:'',updatedAt:''};
});
function item(id: string, name='verification.run', threadId='thread-a', output=''): Item {
  return {id,threadId,turnId:'turn-'+id,kind:'toolCall',status:'inProgress',seq:1,createdAt:'',tool:{callId:id,name,args:{},output}};
}
function emit(event: string, value: Item) { handlers.get(event)!({item:value}); }
function deferred<T>() { let resolve!: (value:T) => void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve}; }
const file = (content: string): ProjectFileContent => ({path:'src/main.ts',content,bytes:content.length,modTime:''});
it('reuses latest verification across tool calls while preserving plan role', () => {
  emit('item/started',item('run-1'));emit('item/completed',item('run-1','verification.run','thread-a','first'));
  emit('item/started',item('run-2'));emit('item/started',item('plan-1','verification.plan'));
  expect(inspector.tabs).toHaveLength(2);emit('item/completed',item('run-2','verification.run','thread-a','latest'));
  expect(inspector.tabs.find(tab=>tab.title==='Test output')?.text).toBe('latest');
});
it('streaming updates and late completions cannot overwrite newer verification', () => {
  emit('item/started',item('old'));emit('item/started',item('new'));
  handlers.get('item/delta')!({threadId:'thread-a',itemId:'new',output:'streaming'});
  emit('item/completed',item('old','verification.run','thread-a','obsolete'));
  expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.text).toBe('streaming');
  emit('item/completed',item('new','verification.run','thread-a','done'));expect(inspector.active?.text).toBe('done');
});
it('verification remains scoped to thread and project', () => {
  emit('item/started',item('a'));emit('item/started',item('b','verification.run','thread-b'));
  chat.main.thread={...chat.main.thread!,projectId:'project-b'};emit('item/started',item('c'));expect(inspector.tabs).toHaveLength(3);
});
it('browser completions reuse logical tab and do not reopen a closed report', () => {
  const report=JSON.stringify({status:'passed',artifacts:[]});
  emit('item/started',item('browser-1','browser.verify'));emit('item/completed',item('browser-1','browser.verify','thread-a',report));
  emit('item/started',item('browser-2','browser.verify'));emit('item/completed',item('browser-2','browser.verify','thread-a',report));
  expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.browser?.status).toBe('passed');
  inspector.closeAll();emit('item/completed',item('browser-2','browser.verify','thread-a',report));expect(inspector.tabs).toHaveLength(0);
});
it('equivalent relative files reuse tabs while projects, worktrees and unresolved parents stay distinct', async () => {
  request.mockResolvedValue(file('content'));
  await inspector.openFile('./src//main.ts','thread-a','project-a');await inspector.openFile('src/main.ts','thread-a','project-a');expect(inspector.tabs).toHaveLength(1);
  await inspector.openFile('src/main.ts','thread-b','project-a');await inspector.openFile('src/main.ts','thread-a','project-b');await inspector.openFile('src/../main.ts','thread-a','project-a');expect(inspector.tabs).toHaveLength(4);
});
it('a reopened file rejects the previous closed read', async () => {
  const old=deferred<ProjectFileContent>(),fresh=deferred<ProjectFileContent>();request.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
  const pending=inspector.openFile('src/main.ts','thread-a');inspector.close(inspector.activeId!);const reopening=inspector.openFile('src/main.ts','thread-a');
  fresh.resolve(file('new'));await reopening;old.resolve(file('stale'));await pending;expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.content).toBe('new');
});
it('closed file reads stay closed and background reads preserve selection', async () => {
  const pending=deferred<ProjectFileContent>();request.mockReturnValue(pending.promise);const reading=inspector.openFile('src/main.ts','thread-a');inspector.closeAll();pending.resolve(file('ignored'));await reading;expect(inspector.tabs).toHaveLength(0);
  const later=deferred<ProjectFileContent>();request.mockReturnValue(later.promise);const background=inspector.openFile('src/main.ts','thread-a');inspector.openOutput('Other','selected','other');later.resolve(file('loaded'));await background;expect(inspector.activeId).toBe('other');
});
it('normalized artifacts reuse scoped tabs and closed image reads stay closed', async () => {
  const artifact: BrowserArtifact={path:'./screens//page.png',kind:'screenshot',mime_type:'image/png',bytes:1};request.mockResolvedValue({mimeType:'image/png',dataB64:'YQ=='});
  await inspector.openArtifact(artifact,'thread-a','project-a');await inspector.openArtifact({...artifact,path:'screens/page.png'},'thread-a','project-a');expect(inspector.tabs).toHaveLength(1);
  await inspector.openArtifact(artifact,'thread-a','project-b');expect(inspector.tabs).toHaveLength(2);
  inspector.closeAll();const pending=deferred<{mimeType:string;dataB64:string}>();request.mockReturnValue(pending.promise);const reading=inspector.openArtifact(artifact,'thread-a','project-a');inspector.closeAll();pending.resolve({mimeType:'image/png',dataB64:'YQ=='});await reading;expect(inspector.tabs).toHaveLength(0);
});
it('turn details in distinct threads remain distinct', async () => {
  request.mockResolvedValue({turn:{id:'same',status:'completed',selection:{},resolved:{},startedAt:'',usage:{inputTokens:0,cachedInputTokens:0,outputTokens:0,reasoningTokens:0,costUsd:0,requests:0}},diagnostics:null,availability:'not_recorded'});await inspector.openTurnDetails('thread-a','same');await inspector.openTurnDetails('thread-b','same');expect(inspector.tabs).toHaveLength(2);
});
it('historical calls reuse by item and remain distinct across equal-length outputs and scopes', () => {
  inspector.openToolOutput('verification.run','first','item-a','thread-a','project-a');
  inspector.openToolOutput('verification.run','grown output','item-a','thread-a','project-a');
  expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.text).toBe('grown output');
  inspector.openToolOutput('verification.run','equal','item-b','thread-a','project-a');
  inspector.openToolOutput('verification.run','equal','item-a','thread-b','project-a');
  inspector.openToolOutput('verification.run','equal','item-a','thread-a','project-b');
  expect(inspector.tabs).toHaveLength(4);emit('item/started',item('auto'));expect(inspector.tabs).toHaveLength(5);
});
it('diffs reuse canonical paths within a turn and keep project and thread scopes', () => {
  const change={path:'./src//main.ts',turnId:'turn-a',action:'modified' as const,additions:1,deletions:0};
  inspector.openDiff(change,'thread-a','project-a');inspector.openDiff({...change,path:'src/main.ts'},'thread-a','project-a');expect(inspector.tabs).toHaveLength(1);
  inspector.openDiff(change,'thread-b','project-a');inspector.openDiff(change,'thread-a','project-b');inspector.openDiff({...change,turnId:'turn-b'},'thread-a','project-a');expect(inspector.tabs).toHaveLength(4);
});
it('newer overlapping reloads win without requiring a close', async () => {
  const old=deferred<ProjectFileContent>(),fresh=deferred<ProjectFileContent>();request.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
  const a=inspector.openFile('src/main.ts','thread-a'),b=inspector.openFile('src/main.ts','thread-a');fresh.resolve(file('new'));await b;old.resolve(file('old'));await a;expect(inspector.active?.content).toBe('new');
});
it('verification completion updates original scope after navigating away', () => {
  emit('item/started',item('background','browser.verify'));chat.main.reset();chat.threads=[];
  emit('item/completed',item('background','browser.verify','thread-a',JSON.stringify({status:'passed',artifacts:[]})));
  expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.browser?.status).toBe('passed');expect(inspector.active?.projectId).toBe('project-a');
});
it('previews reuse their session within scope while preserving different projects and threads', () => {
  const preview={id:'session',threadId:'thread-a',projectId:'project-a',title:'App',url:'http://localhost:3000',status:'ready' as const};
  inspector.openPreview(preview);inspector.openPreview({...preview,title:'Renamed'});expect(inspector.tabs).toHaveLength(1);expect(inspector.active?.title).toBe('Renamed');
  inspector.openPreview({...preview,projectId:'project-b'});inspector.openPreview({...preview,threadId:'thread-b'});expect(inspector.tabs).toHaveLength(3);
  handlers.get('preview/output')!({preview,output:'original scope'});expect(inspector.tabs[0].text).toBe('original scope');expect(inspector.tabs[1].text).toBe('');
});

it('child snapshots reuse child/revision/path tabs and never fall back to mutable project bytes', async () => {
  request.mockResolvedValue(file('owned snapshot'));
  await inspector.openAgentFile('src/main.ts', 'thread-a', 'task-a', 'child-a', 'rev-a', 'project-a');
  await inspector.openAgentFile('src/main.ts', 'thread-a', 'task-a', 'child-a', 'rev-a', 'project-a');
  expect(inspector.tabs).toHaveLength(1); expect(inspector.active?.content).toBe('owned snapshot');
  expect(request).toHaveBeenCalledWith('delegation/file', { path: 'src/main.ts', threadId: 'thread-a', taskId: 'task-a', childId: 'child-a', revision: 'rev-a' });
  await inspector.openAgentFile('src/main.ts', 'thread-a', 'task-a', 'child-b', 'rev-a', 'project-a');
  await inspector.openAgentFile('src/main.ts', 'thread-a', 'task-a', 'child-a', 'rev-b', 'project-a'); expect(inspector.tabs).toHaveLength(3);
  request.mockResolvedValue(undefined); await inspector.openAgentFile('src/main.ts', 'thread-a', 'task-a', 'child-a', 'rev-a', 'project-a');
  expect(inspector.active?.error).toMatch(/snapshot.*unavailable/i); expect(inspector.active?.content).toBeUndefined();
  expect(request.mock.calls.every(([method]) => method === 'delegation/file')).toBe(true);
});
