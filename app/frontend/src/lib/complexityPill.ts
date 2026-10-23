import type { Complexity } from './types';

export type ComplexityLevelId = 'auto' | 'quick' | 'standard' | 'deep';

export const COMPLEXITY_LEVELS: { id: ComplexityLevelId; label: string; hint: string }[] = [
  { id: 'auto', label: 'Auto', hint: 'Pick per message' },
  { id: 'quick', label: 'Quick', hint: 'Fast, little or no thinking' },
  { id: 'standard', label: 'Standard', hint: 'Balanced' },
  { id: 'deep', label: 'Deep', hint: 'More thinking for complex work' },
];

function levelFor(id: Complexity | undefined) {
  return COMPLEXITY_LEVELS.find((level) => level.id === id);
}

/** Pill text: the selected level, else the inherited default, else Auto. */
export function complexityPillLabel(selected: Complexity | undefined, inherited: Complexity | undefined, compact: boolean): string {
  const label = (levelFor(selected) ?? levelFor(inherited) ?? COMPLEXITY_LEVELS[0]).label;
  return compact ? label[0] : label;
}

/** Picking the selected level again clears it so the inherited default applies. */
export function nextComplexity(current: Complexity | undefined, picked: Complexity): Complexity | undefined {
  return current === picked ? undefined : picked;
}
