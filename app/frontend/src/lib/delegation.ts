import type { ModelSelection, UsageTotals } from './types';
export type AgentState = 'queued' | 'running' | 'waiting_approval' | 'completed' | 'failed' | 'cancelled' | 'interrupted';
export interface AgentFinding { path: string; line: number; message: string; severity: string }
export interface AgentCheck { command: string; status: string; summary?: string }
export interface AgentResult { summary: string; findings?: AgentFinding[]; checks?: AgentCheck[]; changedPaths?: string[]; blocker?: string; review?: { reviewerId: string; implementerId: string; revision: string; verdict: string; findings: AgentFinding[] } }
export interface AgentChild { id: string; taskId: string; role: 'implementer' | 'reviewer'; model?: ModelSelection; assignment: { prompt: string; acceptanceChecks?: string[]; contextRefs?: string[] }; state: AgentState; revision?: string; result: AgentResult; usage: UsageTotals; correctionRounds: number; version: number; createdAt: string; updatedAt: string }
export interface AgentTask { id: string; threadId: string; rootTurnId: string; activation: string; state: AgentState; baseRevision: string; projectId?: string; createdAt: string; updatedAt: string }
export interface AgentWorkflow { taskId: string; childId: string; revision: string; checks: (AgentCheck & { label: string; reason?: string; directory?: string; kind?: string })[]; cleanupWarning?: string }
export interface AgentSnapshot { task: AgentTask; children: AgentChild[]; childErrors?: Record<string, string>; childSequences?: Record<string, number>; workflow?: AgentWorkflow; usage?: UsageTotals; budgetAvailable?: boolean; stopReason?: string }
export interface DelegationUpdated { threadId: string; taskId: string; task?: AgentTask; child?: AgentChild; workflow?: AgentWorkflow; error?: string; sequence?: number }
export const agentActive = (state: AgentState) => ['queued', 'running', 'waiting_approval'].includes(state);
export function agentStateLabel(child: AgentChild): string {
  if (child.state === 'failed' && /budget|output.limit|allowance/i.test(child.result.blocker || '')) return 'Budget stopped';
  return ({ waiting_approval: 'Waiting for approval', queued: 'Queued', running: 'Running', completed: 'Completed', failed: 'Failed', cancelled: 'Cancelled', interrupted: 'Interrupted' })[child.state];
}
