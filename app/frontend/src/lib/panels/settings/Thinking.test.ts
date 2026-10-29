import { expect, it } from 'vitest';
import { render } from 'svelte/server';
import Complexity from './Complexity.svelte';
import { app } from '$lib/stores/app.svelte';

it('offers only the default level and default max output, labelled Thinking', () => {
  app.complexity = {
    default: 'deep' as never,
    presets: [],
    limits: { maxDurationMinutes: 60, maxTokens: 1000000, maxCostUsd: 10, maxToolRounds: 100 },
    defaultMaxOutputTokens: 32768,
  };
  const { body } = render(Complexity, { props: {} });
  expect(body).toContain('Default thinking level');
  expect(body).toContain('Low');
  expect(body).toContain('Medium');
  expect(body).toContain('High');
  expect(body).not.toContain('Auto');
  expect(body).not.toContain('Agent teams');
  expect(body).not.toContain('Quick');
  expect(body).toContain('Default max output tokens');
  expect(body).toContain('32768');
});

it('flags an empty or out-of-range max output and disables Save', () => {
  const base = {
    default: 'medium' as const,
    presets: [],
    limits: { maxDurationMinutes: 60, maxTokens: 1000000, maxCostUsd: 10, maxToolRounds: 100 },
  };
  for (const bad of [null, 10, 2_000_000]) {
    app.complexity = { ...base, defaultMaxOutputTokens: bad as never };
    const { body } = render(Complexity, { props: {} });
    expect(body).toContain('Enter a number from 1,024 to 1,000,000');
    expect(body).toMatch(/<button[^>]*disabled[^>]*>Save<\/button>/);
  }
  app.complexity = { ...base, defaultMaxOutputTokens: 32768 };
  const ok = render(Complexity, { props: {} }).body;
  expect(ok).not.toContain('Enter a number from');
  expect(ok).not.toMatch(/<button[^>]*disabled[^>]*>Save<\/button>/);
});

it('offers a No limit checkbox for time and tokens and keeps Save enabled at -1', () => {
  const limits = { maxDurationMinutes: -1, maxTokens: -1, maxCostUsd: 10, maxToolRounds: 100 };
  app.complexity = { default: 'medium' as const, presets: [], limits, defaultMaxOutputTokens: 32768 };
  const { body } = render(Complexity, { props: {} });
  expect(body.match(/type="checkbox"/g)?.length).toBe(2);
  expect(body.match(/No limit/g)?.length).toBe(2);
  expect(body.match(/type="checkbox"[^>]*checked/g)?.length).toBe(2);
  expect(body).toMatch(/type="number"[^>]*min="1" max="480"[^>]*disabled/);
  expect(body).toMatch(/type="number"[^>]*min="10000"[^>]*disabled/);
  expect(body).not.toMatch(/<button[^>]*disabled[^>]*>Save<\/button>/);

  app.complexity = { default: 'medium' as const, presets: [], limits: { ...limits, maxDurationMinutes: 60, maxTokens: 500000 }, defaultMaxOutputTokens: 32768 };
  const finite = render(Complexity, { props: {} }).body;
  expect(finite.match(/type="checkbox"[^>]*checked/g)).toBeNull();
  expect(finite).not.toMatch(/type="number"[^>]*min="1" max="480"[^>]*disabled/);
});
