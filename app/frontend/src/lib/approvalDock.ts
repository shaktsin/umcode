import type { Approval } from '$lib/types';

export type ApprovalDecision = 'approved' | 'approved_always' | 'denied';

export interface ApprovalRecord {
  id: string;
  threadId: string;
  turnId: string;
  summary: string;
  decision: ApprovalDecision;
}

/** The approval the dock shows: this thread's own requests (not agent children's), oldest first. */
export function dockState(approvals: Approval[], threadId: string | null): { current: Approval | null; total: number } {
  if (!threadId) return { current: null, total: 0 };
  const mine = approvals
    .filter((a) => a.threadId === threadId && !a.agentChildId)
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
  return { current: mine[0] ?? null, total: mine.length };
}

export function buildRecord(approval: Approval, approve: boolean, remember: boolean): ApprovalRecord {
  return {
    id: approval.id,
    threadId: approval.threadId,
    turnId: approval.turnId,
    summary: approval.actionSummary || `Run ${approval.tool}`,
    decision: !approve ? 'denied' : remember ? 'approved_always' : 'approved',
  };
}

export function recordLine(record: ApprovalRecord): string {
  const label = record.decision === 'denied' ? 'Denied' : record.decision === 'approved_always' ? 'Approved for this chat' : 'Approved';
  return `${label} · ${record.summary}`;
}

export function recordsFor(records: ApprovalRecord[], threadId: string, turnId: string): ApprovalRecord[] {
  return records.filter((r) => r.threadId === threadId && r.turnId === turnId);
}
