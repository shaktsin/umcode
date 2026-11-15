import { beforeAll, expect, it, vi } from 'vitest';
import { webcrypto } from 'node:crypto';
import { WorkbenchAssistantState } from './assistant.svelte';
import { ImportedEditorState } from './modelImport.svelte';
import { buildContext, graphRevision, previewWorkbenchProposal, type WorkbenchProposal } from './assistant';
import type { ImportedGraph } from './modelImportTypes';
beforeAll(() => vi.stubGlobal('crypto', webcrypto));
function fixture(): ImportedGraph {
  const baselineNodes = [0,1,2].map(i => ({id:'import-'+i,sourceKey:'layers.'+i+'.'+i,kind:'transformer',config:{family:'qwen2',hiddenSize:896,layerStart:i,layerEnd:i},tensorNames:['w'+i]}));
  return {format:'umcode.imported.v1',version:1,source:{family:'qwen2',files:[],config:{},warnings:[],weightsVerified:true,
    tensors:baselineNodes.map((_,i)=>({name:'w'+i,file:'model.safetensors',dtype:'F32',bytes:4,shape:[1]})),baselineNodes},
    nodes: baselineNodes.filter((_,i)=>i!==1).map((b,i)=>({...b,baselineConfig:b.config,label:i?'Qwen2 block 2':'Qwen2 block 0',position:{x:100*i,y:0}})),edges:[]};
}
async function setup() {
  const g=fixture(), text=JSON.stringify(g), editor=new ImportedEditorState(); editor.sync(text);
  const state=new WorkbenchAssistantState(()=>({projectId:'p',modelId:'m',modelName:'Qwen',text:editor.text}));
  const proposal:WorkbenchProposal={projectId:'p',modelId:'m',modelName:'Qwen',revision:await graphRevision(text),explanation:'Recover the original decoder layer.',operations:[
    {type:'restore',nodeId:'import-1'}, {type:'connect',source:'import-0',target:'import-1'}, {type:'connect',source:'import-1',target:'import-2'}]};
  return {g,text,editor,state,proposal};
}
it('captures the selected imported layer and the missing original, even before saving',async()=>{
  const {text}=await setup(); const context=await buildContext('p',{id:'m',name:'Qwen'},text,'import-0');
  expect(context.graphJson).toBe(text); expect(context.summary).toContain('layers.1.1'); expect(context.summary).toContain('Transformer decoder layer 0');
  await expect(buildContext('p',{id:'m',name:'Qwen'},text,'import-1')).rejects.toThrow(/select/i);
});
it('includes MoE internals in assistant context and preserves source bindings when editing an expert setting',async()=>{
  const g=fixture();g.source.family='mixtral';
  const config={family:'mixtral',hiddenSize:16,intermediateSize:32,expertIntermediateSize:32,expertCount:8,expertsPerToken:2,attentionHeads:4,keyValueHeads:2,normalization:'pre RMSNorm',attention:'causal grouped-query self-attention',mlp:'sparse MoE',residual:true};
  g.nodes=g.nodes.map(n=>({...n,label:'Mixtral block 0',config:{...config},baselineConfig:{...config}}));
  const text=JSON.stringify(g),context=await buildContext('p',{id:'m',name:'Mixtral'},text,'import-0');
  expect(context.summary).toContain('Router / top-k selection');expect(context.summary).toContain('2 of 8');
  const proposal:WorkbenchProposal={projectId:'p',modelId:'m',modelName:'Mixtral',revision:await graphRevision(text),explanation:'Try one expert per token.',operations:[{type:'update',nodeId:'import-0',config:{expertsPerToken:1}}]};
  const preview=previewWorkbenchProposal(text,proposal);
  expect(preview.type).toBe('imported');
  if(preview.type==='imported'){expect(preview.graph.nodes[0].config.expertsPerToken).toBe(1);expect(preview.graph.source).toEqual(g.source);}
});
it('restores only from immutable source; preview, apply, undo and redo preserve source and explicit connections',async()=>{
  const {text,state,proposal,editor,g}=await setup();
  state.bindImported(graph=>editor.commit(graph)); state.selectedNodeId='import-0';
  expect(state.selectedLabel).toContain('Transformer decoder layer 0');
  await state.showProposal(proposal); expect(editor.text).toBe(text); expect(state.preview?.changes).toHaveLength(3);
  state.apply(); expect(editor.graph!.nodes.find(n=>n.id==='import-1')).toMatchObject(g.source.baselineNodes[1]);
  expect(editor.graph!.source).toEqual(g.source); expect(editor.graph!.edges).toHaveLength(2);
  editor.undo(); expect(editor.graph!.nodes).toHaveLength(2); editor.redo(); expect(editor.graph!.nodes).toHaveLength(3);
});
it('rejects invalid bindings, cycles, unsupported family edits and stale imported proposals',async()=>{
  const {text,state,g,editor,proposal}=await setup();
  for(const operations of [[{type:'restore',nodeId:'import-0'}],[{type:'restore',nodeId:'bogus'}],[{type:'restore',nodeId:'import-1',tensorNames:['evil']}],
    [...proposal.operations,{type:'connect',source:'import-2',target:'import-0'}]]) {
    expect(()=>previewWorkbenchProposal(text,{...proposal,operations} as WorkbenchProposal)).toThrow();
  }
  expect(()=>previewWorkbenchProposal(JSON.stringify({...g,source:{...g.source,family:'unknown'}}),proposal)).toThrow(/read.only/i);
  state.bindImported(graph=>editor.commit(graph)); await state.showProposal(proposal);
  editor.commit({...g,nodes:g.nodes.slice(1)}); expect(()=>state.apply()).toThrow(/changed/i);
});
it('rejects a restore-and-modify combination during preview, not later at Apply',async()=>{
  const {text,proposal}=await setup();
  expect(()=>previewWorkbenchProposal(text,{...proposal,operations:[{type:'restore',nodeId:'import-1'},{type:'update',nodeId:'import-1',config:{hiddenSize:512}}]})).toThrow(/restore.*separate|separate.*restore/i);
});
