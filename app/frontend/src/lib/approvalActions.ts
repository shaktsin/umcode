import type { Approval } from './types';

/** A task decision approves its stated scope; it is never a reusable tool grant. */
export function approvalActions(approval: Approval) {
  if (approval.kind === 'task_requirement') {
    return { canRemember: false, allowLabel: 'Approve task scope', contextLabel: 'Task scope review' };
  }
  if (approval.kind === 'task_interpretation') {
    const args = approval.args && typeof approval.args === 'object' ? approval.args as Record<string, unknown> : {};
    return { canRemember: false, allowLabel: args.inspection_reason ? 'Confirm inspected state' : 'Confirm interpretation', contextLabel: 'Task interpretation' };
  }
  if (approval.kind === 'workflow') {
    return { canRemember: false, allowLabel: 'Approve solution', contextLabel: 'Workflow review' };
  }
  return {
    canRemember: !approval.kind && Boolean(approval.threadId) && !approval.tool.startsWith('computer.'),
    allowLabel: approval.kind ? 'Approve' : 'Allow once',
    contextLabel: approval.kind ? 'Review request' : approval.tool,
  };
}
