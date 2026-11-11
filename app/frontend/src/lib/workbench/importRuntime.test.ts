import { describe,it,expect } from 'vitest';
import { importAvailability,modelAssets,uploadModelBundle,readRuntimeSnapshot,CompleteModelImport } from './importRuntime';
describe('import completeness and local copying',()=>{
 it.each(['mixtral','granitemoe','qwen2_moe','qwen3_moe'])('accepts complete %s folders but still requires tokenizer assets',family=>{
  expect(importAvailability(family,['config.json','model.safetensors']).complete).toBe(false);
  expect(importAvailability(family,['config.json','model.safetensors','tokenizer.json','tokenizer_config.json']).complete).toBe(true);
 });
 it('keeps a standalone chat template in the model folder upload',()=>{
  const file={name:'chat_template.jinja',size:50,webkitRelativePath:'model/chat_template.jinja'} as File;
  expect(modelAssets([file]).map(a=>a.name)).toEqual(['chat_template.jinja']);
 });
 it('allows metadata inspection of large MoE folders without permitting oversized runtime copying',async()=>{
  const file={name:'model.safetensors',size:90*1024**3,webkitRelativePath:''} as File;
  expect(()=>modelAssets([file],true)).not.toThrow();
  expect(()=>modelAssets([file])).toThrow(/8 GiB/);
  let called=false;
  await expect(uploadModelBundle(async()=>{called=true;return {};},{},[{name:file.name,file}],()=>true,()=>{})).rejects.toThrow(/8 GiB/);
  expect(called).toBe(false);
 });
 it('retains copied files for validation retry and rejects not-ready results',async()=>{
  let uploads=0,ready=false;
  const state=new CompleteModelImport();
  const call=async(_method:string,p:Record<string,unknown>)=>{
   if(p.action==='begin'){uploads++;return {uploadId:'u'};}
   if(p.action==='validate')return {ready,reason:'Missing runtime dependency'};
   return {};
  };
  const file={size:5,slice:()=>new Blob(['hello'])} as File;
  const run=()=>state.run(call,{},[{name:'config.json',file}],()=>true,()=>{});
  await expect(run()).rejects.toThrow('Missing runtime dependency');
  ready=true;await run();expect(uploads).toBe(1);
 });
 it('does not report stale upload progress after Build edits invalidate the model',async()=>{
  let current=true,aborted=false,published=false;
  const progress:number[]=[];
  const file={size:5,slice:()=>new Blob(['hello'])} as File;
  await expect(uploadModelBundle(async(_method,p)=>{
   if(p.action==='begin')return {uploadId:'u'};
   if(p.action==='chunk')current=false;
   if(p.action==='finish')published=true;
   if(p.action==='abort')aborted=true;
   return {};
  },{projectId:'p',modelId:'m',modelCreatedAt:'t'},[{name:'config.json',file}],()=>current,done=>progress.push(done))).rejects.toThrow(/cancelled/);
  expect(progress).toEqual([]);
  expect(aborted).toBe(true);
  expect(published).toBe(false);
 });
 it('observes the completed job before reading its checkpoint readiness',async()=>{
  let completed=false;
  const snapshot=await readRuntimeSnapshot(async(action:string)=>{
   if(action==='job'){completed=true;return {id:'j',status:'completed',epoch:1,epochs:1};}
   return {ready:true,task:'text-generation',reason:'',checkpoint:completed?'new-checkpoint':''};
  });
  expect(snapshot.status.checkpoint).toBe('new-checkpoint');
  expect(snapshot.job?.status).toBe('completed');
 });
 it('requires a config and weights and distinguishes text/vision preprocessing',()=>{
  expect(()=>importAvailability('qwen2',['config.json'])).toThrow(/weights/i);
  expect(()=>importAvailability('resnet',['model.safetensors'])).toThrow(/config.json/i);
  expect(importAvailability('qwen2',['config.json','model.safetensors']).complete).toBe(false);
  expect(importAvailability('qwen2',['config.json','model.safetensors','tokenizer.json','tokenizer_config.json']).complete).toBe(true);
  expect(importAvailability('resnet',['config.json','model.safetensors','preprocessor_config.json']).complete).toBe(true);
  expect(importAvailability('vit',['config.json','model.safetensors','tokenizer.json','tokenizer_config.json']).complete).toBe(false);
 });
 it('allows only model assets and strips one picker folder, not arbitrary traversal',()=>{
  const f=(name:string,path='')=>({name,webkitRelativePath:path,size:1}) as File;
  expect(modelAssets([f('config.json','model/config.json'),f('model.safetensors','model/model.safetensors'),f('evil.py','model/evil.py')]).map(x=>x.name)).toEqual(['config.json','model.safetensors']);
  expect(()=>modelAssets([f('config.json','model/../config.json')])).toThrow();
 });
 it('copies chunks before publishing and aborts on a failed chunk',async()=>{
  let published=false,aborted=false,written=0;
  const call=async(_method:string,p:Record<string,unknown>)=>{
   if(p.action==='begin')return {uploadId:'u'};
   if(p.action==='chunk')written+=atob(p.contentBase64 as string).length;
   if(p.action==='finish'){expect(written).toBe(5);published=true;}
   if(p.action==='abort')aborted=true;
   return {};
  };
  const file={name:'config.json',size:5,webkitRelativePath:'',slice:(a:number,b:number)=>new Blob(['hello'.slice(a,b)])} as File;
  await uploadModelBundle(call,{projectId:'p',modelId:'m',modelCreatedAt:'t'},[{name:'config.json',file}],()=>true,()=>{});
  expect(published).toBe(true);expect(aborted).toBe(false);
  await expect(uploadModelBundle(async(m,p)=>{if(p.action==='chunk')throw Error('disk full');return call(m,p);},{},[{name:'config.json',file}],()=>true,()=>{})).rejects.toThrow(/disk full/);
  expect(aborted).toBe(true);
 });
});
