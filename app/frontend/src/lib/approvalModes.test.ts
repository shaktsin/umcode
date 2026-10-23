import { expect, it } from 'vitest';
import { APPROVAL_MODES, approvalModeLabel } from './approvalModes';

it('labels every approval mode and treats empty as Ask every time', () => {
  expect(approvalModeLabel('')).toBe('Ask every time');
  expect(approvalModeLabel(undefined)).toBe('Ask every time');
  expect(approvalModeLabel('normal')).toBe('Ask every time');
  expect(approvalModeLabel('auto_workspace')).toBe('Auto-approve workspace');
  expect(approvalModeLabel('auto_all')).toBe('Auto-approve all');
});

it('lists three modes with a rule only before auto_all', () => {
  expect(APPROVAL_MODES).toHaveLength(3);
  expect(APPROVAL_MODES.filter((m) => m.separatorBefore).map((m) => m.id)).toEqual(['auto_all']);
});
