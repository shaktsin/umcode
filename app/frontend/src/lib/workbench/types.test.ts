import { describe, expect, it } from 'vitest';
import { describeWorkbenchTask, workbenchTasks } from './types';

describe('Workbench task definitions', () => {
  it('contains the three immutable task choices from the ML Workbench scope', () => {
    expect(workbenchTasks.map((task) => task.id)).toEqual(['text-generation', 'classification', 'custom']);
  });
  it('uses task names from the single task registry', () => {
    expect(describeWorkbenchTask('classification')).toBe('Classification');
  });
});
