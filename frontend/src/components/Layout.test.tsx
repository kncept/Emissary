import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import Layout from './Layout.tsx';

vi.mock('../masquerade.ts', () => ({
  setMasqueradeTarget: vi.fn(),
  clearMasquerade: vi.fn(),
}));

describe('mobile navigation', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = () => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Layout onLogout={() => {}} />
        </MemoryRouter>,
      );
    });
  };

  const nav = () => container.querySelector('nav.main-nav')!;
  const toggle = () => container.querySelector('[aria-label="Toggle menu"]')!;
  const click = (element: Element) => {
    act(() => {
      element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
  });

  it('applies the .open class required by the CSS when toggled', () => {
    mount();
    expect(nav().classList.contains('open')).toBe(false);

    click(toggle());

    expect(nav().classList.contains('open')).toBe(true);
    expect(toggle().getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('.mobile-overlay')).not.toBeNull();
  });

  it('closes the menu from the overlay and when a nav link is chosen', () => {
    mount();

    click(toggle());
    click(container.querySelector('.mobile-overlay')!);
    expect(nav().classList.contains('open')).toBe(false);

    click(toggle());
    click(nav().querySelector('a')!);
    expect(nav().classList.contains('open')).toBe(false);
  });
});
