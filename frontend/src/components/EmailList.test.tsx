import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { markFlagged, markRead } from '../api.ts';
import EmailList from './EmailList.tsx';

vi.mock('../api.ts', () => ({
  getEmails: vi.fn(async () => [{
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
  const row = () => container.querySelector('.email-item')!;

  beforeEach(() => {
    vi.clearAllMocks();
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

    await click(button('Flag'));

    expect(markFlagged).toHaveBeenCalledWith('e1', true);
    expect(row().classList.contains('flagged')).toBe(true);
    expect(button('Remove flag')).not.toBeNull();
  });

  it('rolls back the change and reports an error when the API call fails', async () => {
    vi.mocked(markRead).mockRejectedValueOnce(new Error('network error'));
    await mount();

    await click(button('Mark as read'));

    expect(row().classList.contains('unread')).toBe(true);
    expect(container.textContent).toContain('Failed to update read status');
  });
});
