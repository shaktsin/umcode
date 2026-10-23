import { expect, it } from 'vitest';
import { render } from 'svelte/server';
import PillMenu from './PillMenu.svelte';

const items = [
  { id: 'a', label: 'Alpha item', hint: 'First' },
  { id: 'b', label: 'Beta item', checked: true, separatorBefore: true },
];

it('renders a closed pill with menu semantics and no items', () => {
  const { body } = render(PillMenu, { props: { ariaLabel: 'Pick one', label: 'Standard', items, onselect() {} } });
  expect(body).toContain('Standard');
  expect(body).toContain('class="pill-button');
  expect(body).toContain('aria-haspopup="menu"');
  expect(body).toContain('aria-expanded="false"');
  expect(body).not.toContain('Alpha item');
  expect(body).not.toContain('Beta item');
});

it('renders an icon-only pill labelled by ariaLabel', () => {
  const { body } = render(PillMenu, { props: { ariaLabel: 'More options', items, onselect() {} } });
  expect(body).toContain('aria-label="More options"');
  expect(body).toContain('icon-only');
});
