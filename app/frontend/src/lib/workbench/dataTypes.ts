import type { WorkbenchTask } from './types';

export interface DataFile { name: string; contentBase64: string }
export interface DataSource { kind: 'local' | 'huggingface' | 'example'; name: string; repo?: string; revision?: string; files?: string[] }
export interface DataColumn { name: string; kind: 'number' | 'text' | 'image'; missing: number; uniqueCount: number; examples: string[] }
export interface Dataset { id: string; name: string; source: DataSource; format: 'table' | 'text' | 'images'; rowCount: number; byteCount: number; columns: DataColumn[]; preview: Record<string, string>[]; createdAt: string }
export interface DataConfig {
  datasetId: string; mode: 'features' | 'text' | 'image'; purpose: 'classification' | 'text-generation' | 'regression';
  inputColumns: string[]; targetColumn: string; scale: 'none' | 'standard';
  trainPercent: number; validationPercent: number; testPercent: number; seed: number; stratify: boolean;
  sequenceLength: number; imageWidth: number; imageHeight: number; imageChannels: number;
}
export interface DataSelection { version: string; datasetId: string; config: DataConfig; counts: { train: number; validation: number; test: number }; classes: string[]; statistics: { column: string; mean: number; std: number }[]; createdAt: string; warnings: string[] }
export interface DataState { datasets: Dataset[]; dataset: Dataset | null; selection: DataSelection | null }
export interface HFDataset { id: string; downloads: number; likes: number; private: boolean; gated: boolean }
export interface HFRepository { id: string; sha: string; private: boolean; gated: boolean; files: { name: string; size: number; supported: boolean }[]; license: string }
export interface HFAccount { connected: boolean; username: string }
export type DataSourceTab = 'examples' | 'local' | 'huggingface';
export interface DataContext { projectId: string; modelId: string; task: WorkbenchTask; connected: boolean }
