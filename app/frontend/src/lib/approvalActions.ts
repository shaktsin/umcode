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
    canRemember: !approval.kind && Boolean(approval.threadId) && !approval.tool.startsWith('computer.') && approval.tool !== 'workbench.run_data_script',
    allowLabel: approval.kind ? 'Approve' : 'Allow once',
    contextLabel: approval.kind ? 'Review request' : approval.tool,
  };
}

/** The code an assistant data-script approval asks the person to read, shown as code and not as escaped JSON. */
export function dataScriptReview(approval: Approval): { title: string; outputName: string; script: string } | null {
  if (approval.tool !== 'workbench.run_data_script') return null;
  let args: unknown = approval.args;
  if (typeof args === 'string') { try { args = JSON.parse(args); } catch { return null; } }
  if (!args || typeof args !== 'object') return null;
  const { title, outputName, script } = args as Record<string, unknown>;
  if (typeof script !== 'string') return null;
  return { title: typeof title === 'string' ? title : '', outputName: typeof outputName === 'string' ? outputName : '', script };
}
