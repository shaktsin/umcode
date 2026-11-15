import {describe,it,expect} from 'vitest';
import {render} from 'svelte/server';
import ImportedChatActions from './ImportedChatActions.svelte';

const props={canPromote:false,busy:false,promotion:null,onPromote(){},onOpen(){},onRemove(){}};
describe('imported chat promotion actions',()=>{
 it('keeps promotion disabled until the selected weights pass a prompt test',()=>{
  const blocked=render(ImportedChatActions,{props}).body;
  expect(blocked).toMatch(/<button[^>]*disabled[^>]*>Promote to Chat/);
  const tested=render(ImportedChatActions,{props:{...props,canPromote:true}}).body;
  expect(tested).toContain('Promote to Chat');
  expect(tested).not.toMatch(/<button[^>]*disabled/);
 });
 it('shows the pinned model with open and remove actions after promotion',()=>{
  const promotion={id:'local-1',name:'My model · Original weights',checkpointId:'',runtime:'transformers',projectId:'p',modelId:'m',modelCreatedAt:'v',createdAt:'now'};
  const html=render(ImportedChatActions,{props:{...props,promotion}}).body;
  expect(html).toContain('My model · Original weights');
  expect(html).toContain('Open in Chat');expect(html).toContain('Remove from Chat');
  expect(html).toContain('pinned');
 });
});
