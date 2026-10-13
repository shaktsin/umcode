import { describe, expect, it } from 'vitest';
import { filterSkills, selectSkill, slashQuery } from './skillPicker';

describe('slash skill picker', () => {
  it('opens for a leading slash query at the caret', () => {
    expect(slashQuery('/', 1)).toBe('');
    expect(slashQuery('/review', 7)).toBe('review');
    expect(slashQuery('/plugin:rev', 11)).toBe('plugin:rev');
  });
  it('leaves paths, prose, and completed commands alone', () => {
    for (const text of ['hello /review', '/tmp/file', '/review do this', 'https://example.com']) {
      expect(slashQuery(text, text.length)).toBeNull();
    }
    expect(slashQuery('/review', 0)).toBeNull();
  });
  it('filters valid skills by name and description', () => {
    const skills = [
      { name: 'plugin:review', description: 'Inspect changes' },
      { name: 'other:test', description: 'Review test coverage' },
      { name: 'broken:review', description: 'Review', error: 'invalid' },
    ];
    expect(filterSkills(skills, 'REVIEW').map((s) => s.name)).toEqual(['plugin:review', 'other:test']);
    expect(filterSkills(skills, 'inspect').map((s) => s.name)).toEqual(['plugin:review']);
    expect(filterSkills(skills, 'missing')).toEqual([]);
  });
  it('replaces only the query and preserves the rest of the draft', () => {
    expect(selectSkill('/rev', 4, 'plugin:review')).toEqual({ text: '/plugin:review ', caret: 15 });
    expect(selectSkill('/rev\ncheck my changes', 4, 'plugin:review')).toEqual({
      text: '/plugin:review \ncheck my changes', caret: 15,
    });
  });
});
