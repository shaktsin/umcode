import {beforeAll,beforeEach,expect,it,vi} from 'vitest';
const {call,handlers,toast}=vi.hoisted(()=>({call:vi.fn(),handlers:new Map<string,(p:any)=>void>(),toast:vi.fn()}));
vi.mock('./app.svelte',()=>({app:{call,try:call,toast,onConnected(){},rpc:{on(n:string,f:(p:any)=>void){handlers.set(n,f)}}}}));
let ThreadView:typeof import('./chat.svelte').ThreadView;
beforeAll(async()=>{vi.stubGlobal('window',{addEventListener(){}});({ThreadView}=await import('./chat.svelte'));});
const state={id:'a',threadId:'main',turnId:'t',sequence:2,status:'summarizing',startedAt:'2026-10-20T00:00:00Z',updatedAt:'2026-10-20T00:00:01Z'};
it('hydrates active compaction after reconnect',async()=>{
 call.mockImplementation(async(method:string)=>method==='thread/read'?{thread:{id:'main',settings:{}},turns:[{id:'t',threadId:'main',status:'running'}],items:[],compaction:state}:{tasks:[]});
 const v=new ThreadView();await v.load('main');expect(v.compaction).toMatchObject(state);
 v.applyCompaction({...state,sequence:4,status:'restoring'});await v.load('main');expect(v.compaction?.sequence).toBe(4);
});
it('routes progress independently to main and side chat and ignores stale terminal-turn updates',async()=>{
 const a=new ThreadView(),b=new ThreadView();a.id='main';b.id='side';
 a.applyCompaction(state);b.applyCompaction(state);expect(a.compaction?.id).toBe('a');expect(b.compaction).toBeNull();
 a.applyTurn({id:'t',threadId:'main',status:'failed'} as any);expect(a.compaction?.status).toBe('interrupted');
 a.applyCompaction({...state,sequence:5});expect(a.compaction?.status).toBe('interrupted');
});

beforeEach(()=>{call.mockImplementation(async(method:string,params:Record<string,unknown>)=>method==='thread/start'?{id:'new-thread',projectId:params.projectId,settings:{}}:{tasks:[]});});
it('global new chat sends without the previous project',async()=>{
 const {chat}=await import('./chat.svelte');const {projects}=await import('./projects.svelte');
 projects.activeId='previous-project';chat.main.reset();await chat.newChat();await chat.send('Hello');
 expect(projects.activeId).toBeNull();expect(chat.main.thread?.projectId).toBeUndefined();
});
it('project-specific new chat sends in the chosen project',async()=>{
 const {chat}=await import('./chat.svelte');const {projects}=await import('./projects.svelte');
 projects.activeId='previous-project';chat.main.reset();await chat.newChatFor('chosen-project');await chat.send('Hello');
 expect(chat.main.thread?.projectId).toBe('chosen-project');
});

it('does not clear a newer event with an empty reconnect snapshot',async()=>{
 let resolve!:(v:any)=>void;const read=new Promise(r=>resolve=r);
 call.mockImplementation(async(method:string)=>method==='thread/read'?read:{tasks:[]});
 const v=new ThreadView();v.id='main';const loading=v.load('main');
 v.applyCompaction(state);
 resolve({thread:{id:'main',settings:{}},turns:[{id:'t',threadId:'main',status:'running'}],items:[]});await loading;
 expect(v.compaction?.status).toBe('summarizing');
});
it('does not restore active progress from a snapshot after a terminal turn event',async()=>{
 let resolve!:(v:any)=>void;const read=new Promise(r=>resolve=r);
 call.mockImplementation(async(method:string)=>method==='thread/read'?read:{tasks:[]});
 const v=new ThreadView();v.id='main';v.applyCompaction(state);const loading=v.load('main');
 v.applyTurn({id:'t',threadId:'main',status:'completed'} as any);
 resolve({thread:{id:'main',settings:{}},turns:[{id:'t',threadId:'main',status:'running'}],items:[],compaction:state});await loading;
 expect(v.compaction?.status).not.toBe('summarizing');
});

it('enabling a suggestion responds, then starts the follow-up turn with the returned token', async () => {
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 call.mockImplementation(async(method:string)=>method==='agent/suggestion/respond'?{suggestion:{id:'s1',state:'enabled'},activationToken:'tok',expiresAt:'2999-01-01T00:00:00Z'}:{tasks:[]});
 await v.answerSuggestion('s1','enable');
 const order=call.mock.calls.map(c=>c[0]);
 expect(order.indexOf('agent/suggestion/respond')).toBeLessThan(order.indexOf('turn/start'));
 expect(call).toHaveBeenCalledWith('agent/suggestion/respond',{suggestionId:'s1',decision:'enable'});
 expect(call.mock.calls.find(c=>c[0]==='turn/start')![1]).toMatchObject({threadId:'main',delegationActivationToken:'tok'});
});
it('continuing without agents declines and starts a turn without a token', async () => {
 call.mockClear();const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 call.mockImplementation(async(method:string)=>method==='agent/suggestion/respond'?{suggestion:{id:'s1',state:'declined'}}:{tasks:[]});
 await v.answerSuggestion('s1','decline');
 expect(call).toHaveBeenCalledWith('agent/suggestion/respond',{suggestionId:'s1',decision:'decline'});
 expect(call.mock.calls.find(c=>c[0]==='turn/start')![1]).not.toHaveProperty('delegationActivationToken');
});
it('agent/plan/updated without a status reads the plan back for the matching chat only', async () => {
 call.mockClear();call.mockImplementation(async()=>({tasks:[]}));
 const {chat}=await import('./chat.svelte');chat.main.id='t9';
 const plan={id:'pl',threadId:'t9',taskId:'k',agents:[],limits:{},state:'proposed',createdAt:''};
 handlers.get('agent/plan/updated')!({threadId:'t9',plan});
 expect(call).toHaveBeenCalledWith('agent/plan/get',{planId:'pl'});
 call.mockClear();handlers.get('agent/plan/updated')!({threadId:'other',plan});
 expect(call).not.toHaveBeenCalledWith('agent/plan/get',expect.anything());
});

const enableReply=async(method:string)=>method==='agent/suggestion/respond'?{suggestion:{id:'s1',state:'enabled'},activationToken:'tok',expiresAt:'2999-01-01T00:00:00Z'}:{tasks:[]};
it('enable while the suggesting turn runs keeps the token, queues the follow-up and submits it when the turn completes',async()=>{
 call.mockClear();call.mockImplementation(enableReply);
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;v.applyTurn({id:'t1',threadId:'main',status:'running'} as any);
 await v.answerSuggestion('s1','enable');
 expect(call.mock.calls.some(c=>c[0]==='turn/start')).toBe(false);
 expect(v.queuedFollowUp).toMatch(/Agents are enabled/);expect(v.delegation.token?.activationToken).toBe('tok');
 v.applyTurn({id:'t1',threadId:'main',status:'completed'} as any);
 await vi.waitFor(()=>expect(call.mock.calls.some(c=>c[0]==='turn/start')).toBe(true));
 expect(call.mock.calls.find(c=>c[0]==='turn/start')![1]).toMatchObject({delegationActivationToken:'tok'});
 expect(v.queuedFollowUp).toBeNull();
});
it('a failed turn/start gives the token back',async()=>{
 call.mockClear();toast.mockClear();call.mockImplementation(async(m:string)=>{if(m==='turn/start')throw new Error('still running');return enableReply(m);});
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 await v.answerSuggestion('s1','enable');
 expect(v.delegation.token?.activationToken).toBe('tok');expect(toast).toHaveBeenCalledWith('error',expect.stringMatching(/still running/));
});
it('send returns visibly, not silently, while delegation is pending and the token stays held',async()=>{
 call.mockClear();toast.mockClear();call.mockImplementation(enableReply);
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 await v.delegation.bind('main',undefined);v.delegation.pending=true;
 await v.answerSuggestion('s1','enable');
 expect(toast).toHaveBeenCalledWith('error',expect.stringMatching(/Agents are being prepared/));
 expect(call.mock.calls.some(c=>c[0]==='turn/start')).toBe(false);expect(v.delegation.token?.activationToken).toBe('tok');
});

const fresh=()=>{const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;return v;};
const started=()=>call.mock.calls.some(c=>c[0]==='turn/start');
it('a follow-up queued in one thread is neither shown nor submitted in another',async()=>{
 call.mockClear();call.mockImplementation(enableReply);
 const v=fresh();v.applyTurn({id:'t1',threadId:'main',status:'running'} as any);
 await v.answerSuggestion('s1','enable');expect(v.queuedFollowUp).toBeTruthy();
 v.reset();v.id='other';v.thread={id:'other',settings:{}} as any;
 expect(v.queuedFollowUp).toBeNull();
 v.applyTurn({id:'t2',threadId:'other',status:'completed'} as any);
 await new Promise(r=>setTimeout(r));expect(started()).toBe(false);
 // load() of another thread also drops it
 const w=fresh();w.applyTurn({id:'t1',threadId:'main',status:'running'} as any);await w.answerSuggestion('s1','enable');
 call.mockImplementation(async(m:string)=>m==='thread/read'?{thread:{id:'x',settings:{}},turns:[{id:'t9',threadId:'x',status:'completed'}],items:[]}:{tasks:[]});
 await w.load('x');expect(w.queuedFollowUp).toBeNull();expect(started()).toBe(false);
});
it('an expired activation while queued is shown on the card and clears the queue',async()=>{
 call.mockClear();call.mockImplementation(async(m:string)=>m==='agent/suggestion/respond'?{suggestion:{id:'s1',state:'enabled'},activationToken:'tok',expiresAt:'2000-01-01T00:00:00Z'}:{tasks:[]});
 const v=fresh();v.applyTurn({id:'t1',threadId:'main',status:'running'} as any);
 await v.answerSuggestion('s1','enable');
 v.applyTurn({id:'t1',threadId:'main',status:'completed'} as any);
 await vi.waitFor(()=>expect(v.followUpError).toMatch(/Activation expired/));
 expect(started()).toBe(false);expect(v.queuedFollowUp).toBeNull();
});
it('a reload that finds the turn already completed drains the queue',async()=>{
 call.mockClear();call.mockImplementation(enableReply);
 const v=fresh();v.applyTurn({id:'t1',threadId:'main',status:'running'} as any);
 await v.answerSuggestion('s1','enable');expect(v.queuedFollowUp).toBeTruthy();
 call.mockImplementation(async(m:string)=>m==='thread/read'?{thread:{id:'main',settings:{}},turns:[{id:'t1',threadId:'main',status:'completed'}],items:[]}:{tasks:[]});
 await v.load('main');
 await vi.waitFor(()=>expect(started()).toBe(true));expect(v.queuedFollowUp).toBeNull();
});
