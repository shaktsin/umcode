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
