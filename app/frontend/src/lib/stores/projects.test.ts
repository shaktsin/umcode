import { beforeAll, expect, it, vi } from 'vitest';
const request=vi.hoisted(()=>vi.fn());
vi.mock('./app.svelte',()=>({app:{rpc:{on(){}},onConnected(){},toast(){},call:request,try:request}}));
let projects:typeof import('./projects.svelte').projects;
beforeAll(async()=>{({projects}=await import('./projects.svelte'));});
it('initializes the restored project through the open endpoint',async()=>{
 projects.activeId=null;
 request.mockImplementation(async(method:string)=>method==='project/list'?{projects:[{id:'restored',name:'Restored',root:'/project'}]}:method==='project/files'?{entries:[]}:{id:'restored',name:'Restored',root:'/project'});
 await projects.reload();
 expect(request).toHaveBeenCalledWith('project/open',{projectId:'restored'});
});
