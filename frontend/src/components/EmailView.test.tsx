import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { getEmail, getRawEmail, markRead } from '../api.ts';
import EmailView from './EmailView.tsx';

const emailID = 'users/alice@example.com/inbox/e1.eml';
const rawMime = 'From: alice@example.com\r\nSubject: Hello\r\n\r\nBody\r\n';

vi.mock('../api.ts', () => ({
  getEmail: vi.fn(async () => ({
    id: 'users/alice@example.com/inbox/e1.eml',
    from: 'alice@example.com',
    to: 'bob@example.com',
    subject: 'Hello',
    body: 'Body',
    timestamp: '2026-11-11T00:00:00Z',
    read: false,
    flagged: false,
  })),
  getRawEmail: vi.fn(async () => 'From: alice@example.com\r\nSubject: Hello\r\n\r\nBody\r\n'),
  markRead: vi.fn(async () => {}),
  markFlagged: vi.fn(),
  deleteEmail: vi.fn(),
}));

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((node) => node.textContent === text);
  if (!button) throw new Error(`button "${text}" not found`);
  return button as HTMLButtonElement;
}

describe('EmailView mark as read on open', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={[`/email/${encodeURIComponent(emailID)}`]}>
          <Routes>
            <Route path="/email/:id" element={<EmailView />} />
          </Routes>
        </MemoryRouter>,
      );
    });
  };

  const settle = async () => {
    await act(async () => {});
  };

  beforeEach(() => {
    vi.clearAllMocks();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount();
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
  });

  it('marks an unread email as read when opened', async () => {
    await settle();

    expect(markRead).toHaveBeenCalledWith(emailID, true);
    expect(markRead).toHaveBeenCalledTimes(1);
  });

  it('does not call markRead for an already read email', async () => {
    vi.mocked(markRead).mockClear();
    vi.mocked(getEmail).mockResolvedValueOnce({
      id: emailID,
      from: 'alice@example.com',
      to: 'bob@example.com',
      subject: 'Hello',
      body: 'Body',
      timestamp: '2026-11-11T00:00:00Z',
      read: true,
      flagged: false,
    });

    await settle();

    expect(markRead).not.toHaveBeenCalled();
  });
});

describe('EmailView raw MIME actions', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let createObjectURL: ReturnType<typeof vi.fn>;
  let anchorClick: ReturnType<typeof vi.spyOn>;

  const mount = () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={[`/email/${encodeURIComponent(emailID)}`]}>
          <Routes>
            <Route path="/email/:id" element={<EmailView />} />
          </Routes>
        </MemoryRouter>,
      );
    });
  };

  const settle = async () => {
    await act(async () => {});
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getRawEmail).mockResolvedValue(rawMime);
    createObjectURL = vi.fn(() => 'blob:raw-email');
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() });
    anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});

    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount();
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    anchorClick.mockRestore();
    Reflect.deleteProperty(URL, 'createObjectURL');
    Reflect.deleteProperty(URL, 'revokeObjectURL');
  });

  it('shows the raw message in a modal', async () => {
    await settle();

    click(buttonByText(container, 'View MIME'));
    await settle();

    expect(getRawEmail).toHaveBeenCalledWith(emailID);
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.getAttribute('aria-label')).toBe('Raw email MIME');
    expect(dialog?.textContent).toContain(rawMime);

    // The fetched message is cached, so reopening does not refetch it.
    act(() => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    click(buttonByText(container, 'View MIME'));
    await settle();
    expect(getRawEmail).toHaveBeenCalledTimes(1);
  });

  it('downloads the raw message as an eml file', async () => {
    await settle();

    click(buttonByText(container, 'Download'));
    await settle();

    expect(getRawEmail).toHaveBeenCalledWith(emailID);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = createObjectURL.mock.calls[0][0] as Blob;
    expect(blob.type).toBe('message/rfc822');
    await expect(blob.text()).resolves.toBe(rawMime);
    expect(anchorClick).toHaveBeenCalledTimes(1);
  });

  it('shows an error when the raw message cannot be loaded', async () => {
    vi.mocked(getRawEmail).mockRejectedValueOnce(new Error('boom'));
    await settle();

    click(buttonByText(container, 'View MIME'));
    await settle();

    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Failed to load raw email MIME');
  });
});
