import { expect, it } from 'vitest';
import { COMPLEXITY_LEVELS, complexityPillLabel, nextComplexity } from './complexityPill';

it('labels the pill from the selection, then the inherited default, then Auto', () => {
  expect(complexityPillLabel('deep', 'auto', false)).toBe('Deep');
  expect(complexityPillLabel(undefined, 'standard', false)).toBe('Standard');
  expect(complexityPillLabel(undefined, undefined, false)).toBe('Auto');
  expect(complexityPillLabel(undefined, '' as never, false)).toBe('Auto');
});

it('uses one letter in compact mode and still shows an inherited level', () => {
  expect(complexityPillLabel('quick', 'auto', true)).toBe('Q');
  expect(complexityPillLabel(undefined, 'standard', true)).toBe('S');
});

it('returns to the inherited default when the selected level is picked again', () => {
  expect(nextComplexity('deep', 'deep')).toBeUndefined();
  expect(nextComplexity('auto', 'deep')).toBe('deep');
  expect(nextComplexity(undefined, 'quick')).toBe('quick');
});

it('keeps today\'s four levels and hints', () => {
  expect(COMPLEXITY_LEVELS.map((level) => [level.id, level.label, level.hint])).toEqual([
    ['auto', 'Auto', 'Pick per message'],
    ['quick', 'Quick', 'Fast, little or no thinking'],
    ['standard', 'Standard', 'Balanced'],
    ['deep', 'Deep', 'More thinking for complex work'],
  ]);
});
