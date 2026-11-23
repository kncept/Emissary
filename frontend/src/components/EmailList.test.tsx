import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { markFlagged, markRead } from '../api.ts';
import { clearEmailRefreshRequest } from '../api/email-checks.ts';
import type { Email } from '../types.ts';
import { resetEmailListCache } from '../email-list-cache.ts';
import EmailList from './EmailList.tsx';

vi.mock('../api.ts', () => ({
  getEmails: vi.fn(async (role?: string) => [role === 'sent' ? {
    id: 's1',
    from: 'me@example.com',
    to: 'bob@example.com',
    subject: 'Sent item',
    body: 'Body',
    timestamp: '2026-11-11T00:00:00Z',
    read: true,
    flagged: false,
  } : {
    id: 'e1',
    from: 'alice@example.com',
    to: 'bob@example.com',
    subject: 'Hello',
    body: 'Body',
    timestamp: '2026-11-11T00:00:00Z',
    read: false,
    flagged: false,
  }]),
  deleteEmail: vi.fn(async () => {}),
  markRead: vi.fn(async () => {}),
  markFlagged: vi.fn(async () => {}),
}));

describe('inbox read and flag actions', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = async () => {
    act(() => {
      root!.render(<MemoryRouter><EmailList /></MemoryRouter>);
    });
    await act(async () => {});
  };

  const button = (label: string) => container.querySelector(`[aria-label="${label}"]`)!;
  const click = async (element: Element) => {
    await act(async () => {
      element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  };
  const row = () => container.querySelector('.email-row')!;

  beforeEach(() => {
    vi.clearAllMocks();
    resetEmailListCache();
    clearEmailRefreshRequest();
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

  it('marks an unread email as read via JMAP and updates the row', async () => {
    await mount();
    expect(row().classList.contains('unread')).toBe(true);

    await click(button('Mark as read'));

    expect(markRead).toHaveBeenCalledWith('e1', true);
    expect(row().classList.contains('read')).toBe(true);
    expect(button('Mark as unread')).not.toBeNull();
  });

  it('flags an email via JMAP and updates the row', async () => {
    await mount();

    await click(button('Add star'));

    expect(markFlagged).toHaveBeenCalledWith('e1', true);
    expect(row().classList.contains('flagged')).toBe(true);
    expect(button('Remove star')).not.toBeNull();
  });

  it('rolls back the change and reports an error when the API call fails', async () => {
    vi.mocked(markRead).mockRejectedValueOnce(new Error('network error'));
    await mount();

    await click(button('Mark as read'));

    expect(row().classList.contains('unread')).toBe(true);
    expect(container.textContent).toContain('Failed to update read status');
  });

  it('loads the requested mailbox role instead of always loading the inbox', async () => {
    act(() => {
      root!.render(<MemoryRouter><EmailList role="sent" /></MemoryRouter>);
    });
    await act(async () => {});

    const { getEmails } = await import('../api.ts');
    expect(getEmails).toHaveBeenCalledWith('sent');
    expect(container.textContent).toContain('Sent (1)');
    expect(container.textContent).toContain('Sent item');
    expect(container.textContent).toContain('bob@example.com');
  });
});

describe('inbox caching and refresh', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = async () => {
    act(() => {
      root!.render(<MemoryRouter><EmailList /></MemoryRouter>);
    });
    await act(async () => {});
  };

  beforeEach(() => {
    vi.clearAllMocks();
    resetEmailListCache();
    clearEmailRefreshRequest();
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

  it('restores cached emails without refetching on remount', async () => {
    const { getEmails } = await import('../api.ts');
    await mount();
    expect(getEmails).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('alice@example.com');

    act(() => { root?.unmount(); });
    root = createRoot(container);
    await mount();

    expect(getEmails).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('alice@example.com');
  });

  it('keeps showing emails during a refresh', async () => {
    const { getEmails } = await import('../api.ts');
    await mount();
    expect(container.textContent).toContain('alice@example.com');

    // Simulate slow refresh response
    let resolveRefresh: ((value: Email[]) => void) | null = null;
    const cachedEmail: Email = {
      id: 'e1', from: 'alice@example.com', to: 'bob@example.com', subject: 'Hello',
      body: 'Body', timestamp: '2026-11-11T00:00:00Z', read: false, flagged: false,
    };
    vi.mocked(getEmails).mockImplementationOnce(() => new Promise<Email[]>((resolve) => { resolveRefresh = resolve; }));

    // Trigger refresh via the email-checks module
    const { requestEmailRefresh } = await import('../api/email-checks.ts');
    await act(async () => { requestEmailRefresh(); });

    // Old emails still visible during loading
    expect(container.textContent).toContain('alice@example.com');

    await act(async () => { resolveRefresh!([cachedEmail]); });

    expect(container.textContent).toContain('alice@example.com');
  });
});
