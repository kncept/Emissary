import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import Layout from './Layout.tsx';

vi.mock('../masquerade.ts', () => ({
  setMasqueradeTarget: vi.fn(),
  clearMasquerade: vi.fn(),
}));

vi.mock('../api.ts', () => ({
  getVacationResponse: vi.fn(async () => ({ id: 'singleton', isEnabled: false })),
  setVacationResponse: vi.fn(async () => ({ id: 'singleton', isEnabled: false })),
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

  it('shows a left mailbox panel with drafts and sent folders', () => {
    mount();
    const sidebar = container.querySelector('nav.mailbox-sidebar');
    expect(sidebar).not.toBeNull();

    const hrefs = Array.from(sidebar!.querySelectorAll('.mailbox-link'))
      .map((link) => link.getAttribute('href'));
    expect(hrefs).toEqual(['/', '/drafts', '/sent', '/archive', '/junk', '/trash']);
    expect(sidebar!.querySelector('.mailbox-link.active')?.getAttribute('href')).toBe('/');
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

  it('opens the vacation response modal from the nav item after Compose', async () => {
    mount();

    const vacationButton = Array.from(nav().querySelectorAll('button')).find(
      (item) => item.textContent?.includes('Vacation'),
    )!;
    const composeLink = nav().querySelector('a[href="/compose"]')!;
    const composeItem = composeLink.closest('li');
    expect(composeItem?.nextElementSibling).toBe(vacationButton.closest('li'));

    await act(async () => {
      vacationButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(container.querySelector('[role="dialog"]')).not.toBeNull();
    expect(container.textContent).toContain('Vacation Response');

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });
});
