import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { getEmails, markFlagged, markRead } from '../api.ts';
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

describe('mailbox selection and message open zone', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const rows: Email[] = [
    {
      id: 'e1', from: 'alice@example.com', to: 'bob@example.com', subject: 'Hello',
      body: 'Body', timestamp: '2026-11-11T00:00:00Z', read: false, flagged: false,
    },
    {
      id: 'e2', from: 'carol@example.com', to: 'bob@example.com', subject: 'Second',
      body: 'Body', timestamp: '2026-11-11T00:00:00Z', read: true, flagged: false,
    },
  ];

  const mount = async () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route path="/" element={<EmailList />} />
            <Route path="/email/:id" element={<div>Message opened</div>} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await act(async () => {});
  };

  const clickCheckbox = async (checkbox: HTMLInputElement) => {
    await act(async () => { checkbox.click(); });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    resetEmailListCache();
    clearEmailRefreshRequest();
    vi.mocked(getEmails).mockResolvedValueOnce(rows);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => { root?.unmount(); });
    root = null;
    container.remove();
    localStorage.removeItem('emissary.emailList.density');
  });

  it('selects several rows independently without opening the message', async () => {
    await mount();
    const boxes = Array.from(container.querySelectorAll('input.row-checkbox')) as HTMLInputElement[];
    expect(boxes).toHaveLength(2);
    // The checkbox is a sibling of the open link, never nested inside it.
    expect(boxes[0].closest('a')).toBeNull();

    await clickCheckbox(boxes[0]);
    await clickCheckbox(boxes[1]);

    expect(boxes[0].checked).toBe(true);
    expect(boxes[1].checked).toBe(true);
    expect(container.textContent).toContain('2 selected');
    expect(container.textContent).not.toContain('Message opened');

    await clickCheckbox(boxes[0]);
    expect(boxes[0].checked).toBe(false);
    expect(boxes[1].checked).toBe(true);
    expect(container.textContent).toContain('1 selected');
  });

  it('opens the message from the avatar/content zone', async () => {
    await mount();
    const avatar = container.querySelector('.email-row .sender-avatar')!;
    await act(async () => {
      avatar.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(container.textContent).toContain('Message opened');
  });

  it('keeps the checkbox outside the open link in spacious view too', async () => {
    localStorage.setItem('emissary.emailList.density', 'spacious');
    await mount();
    const checkbox = container.querySelector('.email-item input.row-checkbox') as HTMLInputElement;
    expect(checkbox).not.toBeNull();
    expect(checkbox.closest('a')).toBeNull();

    await clickCheckbox(checkbox);
    expect(checkbox.checked).toBe(true);
    expect(container.textContent).not.toContain('Message opened');
  });
});
