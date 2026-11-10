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

it('agent/plan/updated without a status reads the plan back for the matching chat only', async () => {
 call.mockClear();call.mockImplementation(async()=>({tasks:[]}));
 const {chat}=await import('./chat.svelte');chat.main.id='t9';
 const plan={id:'pl',threadId:'t9',taskId:'k',agents:[],limits:{},state:'proposed',createdAt:''};
 handlers.get('agent/plan/updated')!({threadId:'t9',plan});
 expect(call).toHaveBeenCalledWith('agent/plan/get',{planId:'pl'});
 call.mockClear();handlers.get('agent/plan/updated')!({threadId:'other',plan});
 expect(call).not.toHaveBeenCalledWith('agent/plan/get',expect.anything());
});


it('a plain prompt starts without an activation token or an enabling RPC', async () => {
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 await v.send('Please use agents to build and review this feature');
 expect(call).toHaveBeenCalledWith('turn/start', expect.objectContaining({threadId:'main', text:'Please use agents to build and review this feature'}));
 expect(call.mock.calls.find(c=>c[0]==='turn/start')![1]).not.toHaveProperty('delegationActivationToken');
 expect(call.mock.calls.some(c=>c[0]==='delegation/enable' || c[0]==='agent/suggestion/respond')).toBe(false);
});
it('an approved plan does not start a second turn in response to its status event', async () => {
 const {chat}=await import('./chat.svelte');chat.main.id='main';
 const plan={id:'p1',threadId:'main',taskId:'task',agents:[],limits:{},state:'approved',createdAt:''};
 handlers.get('agent/plan/updated')!({threadId:'main',plan,approvalStatus:'approved'});
 await new Promise(r=>setTimeout(r));
 expect(call.mock.calls.some(c=>c[0]==='turn/start')).toBe(false);
});
it('Continue is available only for an approved plan with a missing continuation and uses its task ID', async () => {
 const v=new ThreadView();v.id='main';v.thread={id:'main',settings:{}} as any;
 v.cards.applyPlanUpdate('p1',{id:'p1',threadId:'main',taskId:'task',agents:[],limits:{} as any,state:'approved',createdAt:''},'approved',true);
 await v.continueApprovedTask('task');
 expect(call).toHaveBeenCalledWith('turn/start',expect.objectContaining({threadId:'main',delegationTaskId:'task'}));
 call.mockClear();await v.continueApprovedTask('other');
 expect(call.mock.calls.some(c=>c[0]==='turn/start')).toBe(false);
});
