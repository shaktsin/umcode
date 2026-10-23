import { describe, expect, it } from 'vitest';
import { approvalActions } from './approvalActions';
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
