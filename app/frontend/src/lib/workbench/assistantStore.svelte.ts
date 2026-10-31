import { workbench } from './store.svelte';
import { WorkbenchAssistantState } from './assistant.svelte';
import { projects } from '$lib/stores/projects.svelte';

export const workbenchAssistant = new WorkbenchAssistantState(() => {
  const model = workbench.selected?.model;
  return model && workbench.projectId && projects.activeId === workbench.projectId ? { projectId: workbench.projectId, modelId: model.id, modelName: model.name, text: workbench.draft } : null;
});
