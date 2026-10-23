import type { ApprovalMode } from './types';

export const APPROVAL_MODES: { id: 'normal' | 'auto_workspace' | 'auto_all'; label: string; hint: string; separatorBefore?: boolean }[] = [
  { id: 'normal', label: 'Ask every time', hint: 'Approve each command and edit' },
  { id: 'auto_workspace', label: 'Auto-approve workspace', hint: 'Edits inside the project folder run freely' },
  { id: 'auto_all', label: 'Auto-approve all', hint: 'Nothing asks. Use with care.', separatorBefore: true },
];

export function approvalModeLabel(mode: ApprovalMode | undefined): string {
  return APPROVAL_MODES.find((m) => m.id === (mode || 'normal'))?.label ?? APPROVAL_MODES[0].label;
}
