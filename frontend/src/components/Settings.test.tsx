import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import type { AuthState } from '../types.ts';
import Settings from './Settings.tsx';

describe('Settings support section', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const render = (auth: AuthState) => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Settings auth={auth} />
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
