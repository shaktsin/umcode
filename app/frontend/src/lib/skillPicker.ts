/** Only a leading, unfinished command opens the picker; paths stay ordinary text. */
export function slashQuery(text: string, caret: number): string | null {
  if (caret < 1 || caret > text.length) return null;
  const prefix = text.slice(0, caret);
  if (!/^\/[a-z0-9:._-]*$/i.test(prefix)) return null;
  if (text[caret] && !/\s/.test(text[caret])) return null;
  return prefix.slice(1);
}

export function filterSkills<T extends { name: string; description: string; error?: string }>(skills: T[], query: string): T[] {
  const needle = query.toLowerCase();
  return skills.filter((skill) => !skill.error && `${skill.name} ${skill.description}`.toLowerCase().includes(needle));
}

export function selectSkill(text: string, caret: number, name: string): { text: string; caret: number } {
  const prefix = `/${name} `;
  return { text: prefix + text.slice(caret), caret: prefix.length };
}
