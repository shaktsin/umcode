import { describe, expect, it } from 'vitest';
import { approvalActions, dataScriptReview } from './approvalActions';
import type { Approval } from './types';
const approval = (kind = '', tool = 'shell.run', args: unknown = {}): Approval => ({ id:'a', threadId:'t', turnId:'turn', itemId:'i', kind, tool, args, risk:'red', reason:'', actionSummary:'', status:'pending', createdAt:'', expiresAt:'' });
describe('approval scope presentation', () => {
  it('does not offer chat-wide permission for task scope or interpretation decisions', () => {
    expect(approvalActions(approval('task_requirement'))).toEqual({canRemember:false, allowLabel:'Approve task scope', contextLabel:'Task scope review'});
    expect(approvalActions(approval('task_interpretation')).canRemember).toBe(false);
    expect(approvalActions(approval('workflow')).canRemember).toBe(false);
  });
  it('names acknowledgement as accepting inspected state rather than successful execution', () => {
    const result = approvalActions(approval('task_interpretation','task.propose',{operation:{id:'op'},inspection_reason:'Effect checked'}));
    expect(result.allowLabel).toBe('Confirm inspected state');
  });
  it('keeps normal exact-action approval and computer-use restrictions', () => {
    expect(approvalActions(approval()).canRemember).toBe(true);
    expect(approvalActions(approval()).allowLabel).toBe('Allow once');
    expect(approvalActions(approval('', 'computer.act')).canRemember).toBe(false);
  });
});

describe('assistant data script approvals', () => {
  const scriptArgs = { title: 'Balance reviews', inputDatasetIds: ['a'], outputName: 'balanced', script: "rows = load()\nprint('kept')", expectOutput: 'dataset' };
  it('never offers to remember an allow, because each script is different code', () => {
    expect(approvalActions(approval('', 'workbench.run_data_script', scriptArgs)).canRemember).toBe(false);
    expect(approvalActions(approval('', 'workbench.import_data', {})).canRemember).toBe(true);
  });
  it('exposes the script as readable code, decoding string or object args', () => {
    expect(dataScriptReview(approval('', 'workbench.run_data_script', scriptArgs))).toEqual({ title: 'Balance reviews', outputName: 'balanced', script: "rows = load()\nprint('kept')" });
    expect(dataScriptReview(approval('', 'workbench.run_data_script', JSON.stringify(scriptArgs)))?.script).toBe("rows = load()\nprint('kept')");
    expect(dataScriptReview(approval('', 'shell.run', scriptArgs))).toBeNull();
    expect(dataScriptReview(approval('', 'workbench.run_data_script', '{broken'))).toBeNull();
  });
});
