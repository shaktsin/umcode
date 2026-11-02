/** Local CPU training RPC contract. Runs pin saved graph and preparation. */
export interface TrainingContext { projectId: string; modelId: string; modelCreatedAt: string }
export interface TrainingConfig { epochs: number; batchSize: number; learningRate: number; seed: number }
export interface TrainingMetrics { loss: number; mae?: number; rmse?: number; r2?: number; accuracy?: number; macroF1?: number; perplexity?: number; tokenAccuracy?: number; samples?: number }
export interface TrainingEpoch { epoch: number; train: TrainingMetrics; validation: TrainingMetrics | null }
export interface TrainingRun { id: string; name: string; modelCreatedAt: string; status: 'running' | 'completed' | 'cancelled' | 'failed'; config: TrainingConfig; epoch: number; history: TrainingEpoch[]; train?: TrainingMetrics | null; validation?: TrainingMetrics | null; test?: TrainingMetrics | null; checkpointId?: string; error?: string; createdAt: string; finishedAt?: string; durationMs: number; preparationVersion: string; parameterCount?: number }
export interface TrainingState { runs: TrainingRun[]; runtimeAvailable: boolean; issues: string[] }
export type TrainingCall = (method: string, params: Record<string, unknown>, timeoutMs?: number) => Promise<unknown>;
