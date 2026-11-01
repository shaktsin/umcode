/** Exact workbench/inference RPC JSON contract. Checkpoints pin saved graph and Data preparation. */
export interface InferenceContext { projectId: string; modelId: string; modelCreatedAt: string }
export interface CheckpointInput { mode: string; features: string[]; sequenceLength: number; imageWidth: number; imageHeight: number; imageChannels: number; classes: string[] }
export interface Checkpoint { id: string; name: string; createdAt: string; graphRevision: string; modelCreatedAt: string; task: string; format: string; byteCount: number; chatCompatible: boolean; input: CheckpointInput }
export interface RuntimeInfo { id: string; name: string; available: boolean; reason?: string }
export interface Promotion extends InferenceContext { id: string; checkpointId: string; name: string; runtime: string; createdAt: string }
export interface InferenceState { checkpoints: Checkpoint[]; runtimes: RuntimeInfo[]; loadedCheckpointId: string; promotion?: Promotion; issues: string[] }
export interface Prediction { checkpointId: string; testId: string; text?: string; value?: number; label?: string; scores?: { label: string; value: number }[]; device: string; durationMs: number; truncated?: boolean }
export interface CheckpointFile { name: string; size: number; arrayBuffer(): Promise<ArrayBuffer> }
export type InferenceCall = (method: string, params: Record<string, unknown>, timeoutMs?: number) => Promise<unknown>;

export interface ArchitectureTest { inputShape: number[]; outputShape: number[]; parameterCount: number; device: string; untrained: boolean }
