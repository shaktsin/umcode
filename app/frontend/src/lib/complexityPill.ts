import type { Complexity } from './types';
import type { PillMenuItem } from './pillMenu';

export type ComplexityLevelId = 'low' | 'medium' | 'high';

export const COMPLEXITY_LEVELS: { id: ComplexityLevelId; label: string; hint: string }[] = [
  { id: 'low', label: 'Low', hint: 'Fast, minimal thinking' },
  { id: 'medium', label: 'Medium', hint: 'Balanced' },
  { id: 'high', label: 'High', hint: 'More thinking for complex work' },
];

/** Maps stored or legacy values onto a level; empty or unknown means "inherit". */
export function normalizeLevel(value: string | undefined | null): ComplexityLevelId | undefined {
  switch ((value ?? '').trim().toLowerCase()) {
    case 'low':
    case 'quick':
      return 'low';
    case 'medium':
    case 'standard':
    case 'auto':
      return 'medium';
    case 'high':
    case 'deep':
      return 'high';
    default:
      return undefined;
  }
}

function levelFor(id: ComplexityLevelId | undefined) {
  return COMPLEXITY_LEVELS.find((level) => level.id === id);
}

/** Pill text: the selected level, else the inherited default, else Medium. */
export function complexityPillLabel(selected: Complexity | undefined, inherited: Complexity | undefined, compact: boolean): string {
  const label = (levelFor(normalizeLevel(selected)) ?? levelFor(normalizeLevel(inherited)) ?? COMPLEXITY_LEVELS[1]).label;
  return compact ? label[0] : label;
}

/** Picking the selected level again clears it so the inherited default applies. */
export function nextComplexity(current: Complexity | undefined, picked: Complexity): Complexity | undefined {
  return normalizeLevel(current) === normalizeLevel(picked) ? undefined : picked;
}

const order: ComplexityLevelId[] = ['low', 'medium', 'high'];

/** The supported level closest to `level`, preferring the lower one on a tie. */
function nearestSupported(level: ComplexityLevelId, supported: ComplexityLevelId[]): ComplexityLevelId {
  const at = order.indexOf(level);
  return [...supported].sort((a, b) => Math.abs(order.indexOf(a) - at) - Math.abs(order.indexOf(b) - at) || order.indexOf(a) - order.indexOf(b))[0];
}

/**
 * Menu items for the pill. `supported` is the active model's level list; when it
 * is empty or unknown every level stays selectable.
 */
export function levelMenuItems(selected: Complexity | undefined, inherited: Complexity | undefined, supported?: string[]): PillMenuItem[] {
  const known = (supported ?? []).map((s) => normalizeLevel(s)).filter((s): s is ComplexityLevelId => !!s);
  const checked = normalizeLevel(selected);
  return COMPLEXITY_LEVELS.map((level) => {
    const unsupported = known.length > 0 && !known.includes(level.id);
    const item: PillMenuItem = { id: level.id, label: level.label, hint: level.hint, checked: checked === level.id };
    if (unsupported) {
      item.disabled = true;
      item.title = `Not supported by this model; ${levelFor(nearestSupported(level.id, known))!.label} is the nearest.`;
    }
    return item;
  });
}
