export type ImportConfig = Record<string, number | string | boolean>;
export interface ImportFile { name: string; size: number; kind: 'config' | 'index' | 'weights' | 'tokenizer' | 'processor'; content?: string; header?: string }
export interface ImportTensor { name: string; dtype: string; shape: number[]; file: string; bytes: number }
export interface ImportBaseline { id: string; kind: string; config: ImportConfig; tensorNames: string[]; sourceKey?: string }
export interface ImportedNode extends ImportBaseline { label: string; position: { x: number; y: number }; baselineConfig: ImportConfig }
export interface ImportedGraph {
  version: 1; format: 'umcode.imported.v1';
  source: { family: string; config: Record<string, unknown>; files: { name: string; size: number }[]; tensors: ImportTensor[]; weightsVerified: boolean; warnings: string[]; baselineNodes: ImportBaseline[] };
  nodes: ImportedNode[]; edges: { id: string; source: string; target: string }[];
}
export interface ImportInspection { family: string; name: string; supported: boolean; weightsVerified: boolean; warnings: string[]; tensorCount: number; parameterCount: number; totalWeightBytes: number; graph: ImportedGraph }
export interface ImportedPaletteItem { kind: string; label: string; description: string; config: ImportConfig }
