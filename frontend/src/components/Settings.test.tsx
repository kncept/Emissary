import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { changeOwnPassword, isPasswordChangeSupported } from '../api.ts';
import type { AuthState } from '../types.ts';
import Settings from './Settings.tsx';

vi.mock('../api.ts', () => ({
  changeOwnPassword: vi.fn(async () => ({})),
  isPasswordChangeSupported: vi.fn(async () => true),
}));

describe('Settings support section', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const render = async (
    auth: AuthState,
    onLogout: () => void | Promise<void> = () => {},
  ) => {
    await act(async () => {
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

  it('shows Support for a System Manager', async () => {
    await render({ authenticated: true, username: 'manager@example.com', systemManager: true });

    expect(supportLink()).not.toBeNull();
  });

  it('hides Support from users who are not System Managers', async () => {
    await render({ authenticated: true, username: 'user@example.com' });

    expect(supportLink()).toBeNull();
    expect(container.textContent).not.toContain('Support');
  });
});

describe('Settings logout', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const render = async (onLogout: () => void | Promise<void>) => {
    await act(async () => {
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
    await render(onLogout);

    await click(logoutButton());

    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it('keeps the screen usable and reports a failed logout', async () => {
    const onLogout = vi.fn(async () => { throw new Error('network error'); });
    await render(onLogout);

    await click(logoutButton());

    expect(container.textContent).toContain('Failed to log out');
    expect(logoutButton().disabled).toBe(false);
  });
});

describe('Settings change password', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const auth: AuthState = { authenticated: true, username: 'user@example.com' };

  const render = async (onLogout: () => void | Promise<void> = () => {}) => {
    await act(async () => {
      root!.render(
        <MemoryRouter>
          <Settings auth={auth} onLogout={onLogout} />
        </MemoryRouter>,
      );
    });
  };

  const button = (label: string) =>
    Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes(label));

  const field = (id: string) => container.querySelector(`#${id}`) as HTMLInputElement;

  const click = async (element: Element | undefined) => {
    expect(element).toBeDefined();
    await act(async () => {
      element!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  };

  const setValue = (input: HTMLInputElement, value: string) => {
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
      setter.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
  };

  const submit = async () => {
    const form = container.querySelector('form')!;
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    vi.clearAllMocks();
    vi.mocked(isPasswordChangeSupported).mockReset().mockResolvedValue(true);
    vi.mocked(changeOwnPassword).mockReset().mockResolvedValue({});
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    document.body.replaceChildren();
  });

  it('hides the password UI when the backend does not support it', async () => {
    vi.mocked(isPasswordChangeSupported).mockResolvedValueOnce(false);
    await render();

    expect(isPasswordChangeSupported).toHaveBeenCalledTimes(1);
    expect(button('Change password')).toBeUndefined();
    expect(container.textContent).not.toContain('Password');
  });

  it('checks password support only once per mount', async () => {
    await render();

    expect(isPasswordChangeSupported).toHaveBeenCalledTimes(1);
  });

  it('offers the change password form to an ordinary user', async () => {
    await render();

    await click(button('Change password'));

    expect(field('settings-current-password')).not.toBeNull();
    expect(field('settings-new-password')).not.toBeNull();
    expect(field('settings-confirm-password')).not.toBeNull();
  });

  it('submits the current and new password', async () => {
    await render();
    await click(button('Change password'));
    setValue(field('settings-current-password'), 'old-pass');
    setValue(field('settings-new-password'), 'new-pass');
    setValue(field('settings-confirm-password'), 'new-pass');

    await submit();

    expect(changeOwnPassword).toHaveBeenCalledWith({ oldPassword: 'old-pass', newPassword: 'new-pass' });
    expect(container.textContent).toContain('Password changed');
    expect(button('Sign in again')).toBeDefined();
  });

  it('rejects a mismatched confirmation without calling the API', async () => {
    await render();
    await click(button('Change password'));
    setValue(field('settings-current-password'), 'old-pass');
    setValue(field('settings-new-password'), 'new-pass');
    setValue(field('settings-confirm-password'), 'different');

    await submit();

    expect(changeOwnPassword).not.toHaveBeenCalled();
    expect(container.textContent).toContain('New passwords do not match');
  });

  it('shows the API error when the backend does not support password changes', async () => {
    vi.mocked(changeOwnPassword).mockRejectedValueOnce(
      new Error('Password changes are not supported by the configured user backend.'),
    );
    await render();
    await click(button('Change password'));
    setValue(field('settings-current-password'), 'old-pass');
    setValue(field('settings-new-password'), 'new-pass');
    setValue(field('settings-confirm-password'), 'new-pass');

    await submit();

    expect(container.textContent).toContain('not supported by the configured user backend');
    expect(button('Sign in again')).toBeUndefined();
  });

  it('signs the user out so they log in with the new password', async () => {
    const onLogout = vi.fn(async () => {});
    await render(onLogout);
    await click(button('Change password'));
    setValue(field('settings-current-password'), 'old-pass');
    setValue(field('settings-new-password'), 'new-pass');
    setValue(field('settings-confirm-password'), 'new-pass');
    await submit();

    await click(button('Sign in again'));

    expect(onLogout).toHaveBeenCalledTimes(1);
  });
});
