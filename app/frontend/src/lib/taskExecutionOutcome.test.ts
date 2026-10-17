import { describe, expect, it } from 'vitest';
import type { Item } from './types';
import { executionOutcomePresentation, isProvisionalExecution, taskFailurePresentation } from './taskOutcome';

const item = (id: string, text: string, data: unknown): Item => ({ id, threadId:'th', turnId:'turn',seq:1,kind:'agentMessage',status:'completed',text,createdAt:'',data });
describe('authoritative execution outcomes', () => {
 it('explains missing execution without inventing permissions', () => {
  const p=executionOutcomePresentation([item('final','unsupported text',{taskOutcome:{status:'not_executed',expectationIds:['e1'],evidenceIds:[]}})],'failed');
  expect(p.show).toBe(true);
  expect(p.text).toContain('did not execute');
  expect(p.text).not.toContain('permission');
 });
 it('distinguishes executed but unverified effects', () => {
  const p=executionOutcomePresentation([item('final','',{taskOutcome:{status:'unverified',expectationIds:['e1']}})],'failed');
  expect(p.text).toContain('verification');
  expect(p.text).not.toContain('did not execute');
 });
 it('preserves provisional narration for audit while marking it for omission', () => {
  const provisional=item('draft','[Created fake.txt]',{provisionalExecution:true,provisionalText:'[Created fake.txt]'});
  expect(isProvisionalExecution(provisional)).toBe(true);
  const final=item('final','The model did not execute the requested action.',{taskOutcome:{status:'not_executed'},actionFailure:{category:'not_executed',blockerId:'expectation:turn',message:'No action ran',retryable:false}});
  expect(executionOutcomePresentation([provisional,final],'failed').details).toContain('[Created fake.txt]');
  expect(taskFailurePresentation([provisional,final],'').details).toContain('[Created fake.txt]');
 });
 it('does not turn quoted receipt text into execution evidence', () => {
  const quote=item('quote','Example: [Created file.txt]',undefined);
  expect(isProvisionalExecution(quote)).toBe(false);
  expect(executionOutcomePresentation([quote],'completed').show).toBe(false);
 });
 it('leaves explanations quiet and reports actual verification evidence', () => {
  expect(executionOutcomePresentation([item('explain','Explanation',{taskOutcome:{status:'satisfied',evidenceIds:[]}})],'completed').show).toBe(false);
  expect(executionOutcomePresentation([item('verified','',{taskOutcome:{status:'satisfied',evidenceIds:['actual-read']}})],'completed').text).toContain('Verified');
 });
 it('renders the same authoritative state after stream replacement and reload', () => {
  const items=[item('draft','fake receipt',{provisionalExecution:true}),item('final','',{taskOutcome:{status:'not_executed'}})];
  expect(executionOutcomePresentation(items,'failed')).toEqual(executionOutcomePresentation(JSON.parse(JSON.stringify(items)),'failed'));
 });
});
