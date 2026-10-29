import { expect, it } from 'vitest';
import { COMPLEXITY_LEVELS, complexityPillLabel, levelMenuItems, nextComplexity, normalizeLevel } from './complexityPill';

it('lists exactly Low, Medium and High with hints', () => {
  expect(COMPLEXITY_LEVELS.map((level) => [level.id, level.label])).toEqual([
    ['low', 'Low'],
    ['medium', 'Medium'],
    ['high', 'High'],
  ]);
  expect(COMPLEXITY_LEVELS.every((level) => level.hint.length > 0)).toBe(true);
});

it('labels the pill from the selection, then the inherited default, then Medium', () => {
  expect(complexityPillLabel('high', 'low', false)).toBe('High');
  expect(complexityPillLabel(undefined, 'low', false)).toBe('Low');
  expect(complexityPillLabel(undefined, undefined, false)).toBe('Medium');
  expect(complexityPillLabel(undefined, '', false)).toBe('Medium');
});

it('uses one letter in compact mode and still shows an inherited level', () => {
  expect(complexityPillLabel('low', 'medium', true)).toBe('L');
  expect(complexityPillLabel(undefined, 'high', true)).toBe('H');
});

it('maps legacy stored levels through normalizeLevel', () => {
  expect(normalizeLevel('deep')).toBe('high');
  expect(normalizeLevel('quick')).toBe('low');
  expect(normalizeLevel('standard')).toBe('medium');
  expect(normalizeLevel('auto')).toBe('medium');
  expect(normalizeLevel('')).toBeUndefined();
  expect(normalizeLevel(undefined)).toBeUndefined();
  expect(complexityPillLabel('deep' as never, undefined, false)).toBe('High');
  expect(complexityPillLabel(undefined, 'quick' as never, false)).toBe('Low');
});

it('returns to the inherited default when the selected level is picked again', () => {
  expect(nextComplexity('high', 'high')).toBeUndefined();
  expect(nextComplexity('deep' as never, 'high')).toBeUndefined();
  expect(nextComplexity('low', 'high')).toBe('high');
  expect(nextComplexity(undefined, 'low')).toBe('low');
});

it('renders levels the model does not support as disabled and names the nearest one', () => {
  const items = levelMenuItems(undefined, 'medium', ['low', 'medium']);
  expect(items.map((i) => [i.id, i.disabled ?? false])).toEqual([['low', false], ['medium', false], ['high', true]]);
  expect(items[2].title).toContain('Medium');
  const onlyHigh = levelMenuItems(undefined, 'medium', ['high']);
  expect(onlyHigh[0].disabled).toBe(true);
  expect(onlyHigh[0].title).toContain('High');
});

it('keeps every level enabled when support is unknown and marks the checked one', () => {
  for (const supported of [undefined, []]) {
    const items = levelMenuItems('deep' as never, undefined, supported);
    expect(items.some((i) => i.disabled)).toBe(false);
    expect(items.filter((i) => i.checked).map((i) => i.id)).toEqual(['high']);
  }
});
