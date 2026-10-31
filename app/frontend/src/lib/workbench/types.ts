export type WorkbenchTask = 'text-generation' | 'classification' | 'custom';

export interface WorkbenchModel {
  schemaVersion: number;
  id: string;
  name: string;
  task: WorkbenchTask;
  createdAt: string;
}

export interface OpenWorkbenchModel {
  model: WorkbenchModel;
  /** Exact editor text; graph stays available for existing RPC clients. */
  graphJson?: string;
  graph: { version: number; nodes: unknown[]; edges: unknown[]; [key: string]: unknown };
}

export const workbenchTasks: { id: WorkbenchTask; name: string; description: string; input: string; output: string }[] = [
  { id: 'text-generation', name: 'Text generation', description: 'Continue text or produce responses from prompts.', input: 'text', output: 'text' },
  { id: 'classification', name: 'Classification', description: 'Predict labels from text, images, or feature vectors.', input: 'text / image / features', output: 'label' },
  { id: 'custom', name: 'Custom', description: 'Build your own architecture for the input you choose.', input: 'your input', output: 'your output' },
];

export function describeWorkbenchTask(task: WorkbenchTask): string {
  return workbenchTasks.find((item) => item.id === task)?.name ?? task;
}
