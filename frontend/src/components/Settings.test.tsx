import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import type { AuthState } from '../types.ts';
import Settings from './Settings.tsx';

describe('Settings support section', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const render = (
    auth: AuthState,
    onLogout: () => void | Promise<void> = () => {},
  ) => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Settings auth={auth} onLogout={onLogout} />
        </MemoryRouter>,
      );
    });
  };

  const supportLink = () => container.querySelector('a[href="/support"]');

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
    document.body.replaceChildren();
  });

  it('shows Support for a System Manager', () => {
    render({ authenticated: true, username: 'manager@example.com', systemManager: true });

    expect(supportLink()).not.toBeNull();
  });

  it('hides Support from users who are not System Managers', () => {
    render({ authenticated: true, username: 'user@example.com' });

    expect(supportLink()).toBeNull();
    expect(container.textContent).not.toContain('Support');
  });
});

describe('Settings logout', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const render = (onLogout: () => void | Promise<void>) => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Settings auth={{ authenticated: true, username: 'user@example.com' }} onLogout={onLogout} />
        </MemoryRouter>,
      );
    });
  };

  const logoutButton = () =>
    Array.from(container.querySelectorAll('button')).find((button) => button.textContent?.includes('Logout'))!;

  const click = async (button: Element) => {
    await act(async () => {
      button.dispatchEvent(new MouseEvent('click', { bubbles: true }));
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
    document.body.replaceChildren();
    vi.restoreAllMocks();
  });

  it('uses the application logout handler instead of just navigating to /login', async () => {
    const onLogout = vi.fn(async () => {});
    render(onLogout);

    await click(logoutButton());

    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it('keeps the screen usable and reports a failed logout', async () => {
    const onLogout = vi.fn(async () => { throw new Error('network error'); });
    render(onLogout);

    await click(logoutButton());

    expect(container.textContent).toContain('Failed to log out');
    expect(logoutButton().disabled).toBe(false);
  });
});
