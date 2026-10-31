import { addLayer, emptyGraph, starterGraph, type Graph, type StarterPreset } from './graph';
import type { WorkbenchTask } from './types';

export type InputModality = 'text' | 'image' | 'features';
export type StartingPoint = 'blank' | StarterPreset;
export const inputModalities: { id: InputModality; name: string }[] = [{ id: 'text', name: 'Text' }, { id: 'image', name: 'Vision (images)' }, { id: 'features', name: 'Feature vectors' }];
const names: Record<StartingPoint, string> = { blank: 'Blank canvas', text: 'Small text model', classifier: 'Small text classifier', gpt2: 'GPT-2-size architecture', cnn: 'Small CNN', vit: 'Small Vision Transformer', mlp: 'Feature classifier' };
export function relevantStarterPresets(task: WorkbenchTask, input: InputModality): { id: StartingPoint; name: string }[] {
  const ids: StartingPoint[] = task === 'text-generation' ? ['blank', 'text', 'gpt2'] : input === 'image' ? ['blank', 'cnn', 'vit'] : input === 'features' ? ['blank', 'mlp'] : task === 'classification' ? ['blank', 'classifier'] : ['blank', 'text', 'classifier'];
  return ids.map(id => ({ id, name: names[id] }));
}
export function modelGraph(preset: StartingPoint, input: InputModality): Graph {
  if (preset !== 'blank') return starterGraph(preset);
  if (input === 'text') return emptyGraph();
  const graph = addLayer(emptyGraph(), 'input', { x: 80, y: 60 });
  graph.nodes[0].config.mode = input;
  return graph;
}
