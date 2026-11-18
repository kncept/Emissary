import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { getEmail, sendEmail } from '../api.ts';
import type { Email } from '../types.ts';
import Compose from './Compose.tsx';

vi.mock('../api.ts', () => ({
  getEmail: vi.fn(),
  sendEmail: vi.fn(async () => ({ success: true })),
  MASQUERADE_AS_KEY: 'masquerade_as',
  parseAddressList: (input: string) =>
    input.split(',').map((part) => part.trim()).filter((part) => part.length > 0),
}));

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((node) => node.textContent === text);
  if (!button) throw new Error(`button "${text}" not found`);
  return button as HTMLButtonElement;
}

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function inputValue(id: string, value: string) {
  const input = document.getElementById(id) as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
  act(() => {
    setter.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

function originalEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'orig-id',
    from: 'alice@example.com',
    to: 'me@example.com',
    subject: 'Hello there',
    body: 'Original text',
    timestamp: '2026-01-01T00:00:00Z',
    read: true,
    flagged: false,
    messageId: '<orig@example.com>',
    references: ['<root@example.com>'],
    replyTo: ['replyto@example.com'],
    ...overrides,
  };
}

async function flush() {
  await act(async () => {});
}

describe('Compose From lock', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root!.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('locks the From field to the current user', () => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose currentUser="me@example.com" />
        </MemoryRouter>,
      );
    });
    const from = container.querySelector('#from') as HTMLInputElement;
    expect(from.value).toBe('me@example.com');
    expect(from.readOnly).toBe(true);
  });

  it('locks the From field to the masquerade target when one is set', () => {
    window.localStorage.setItem('masquerade_as', 'victim@example.com');
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose currentUser="admin@example.com" />
        </MemoryRouter>,
      );
    });
    const from = container.querySelector('#from') as HTMLInputElement;
    expect(from.value).toBe('victim@example.com');
    expect(from.readOnly).toBe(true);
  });
});

describe('Compose reply and forward prefill', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root!.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('prefills a reply with the reply-to address, RE: subject and quoted body', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail());
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/orig-id']}>
          <Routes>
            <Route path="/compose/:id" element={<Compose mode="reply" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();

    expect(getEmail).toHaveBeenCalledWith('orig-id');
    expect((container.querySelector('#from') as HTMLInputElement).value).toBe('me@example.com');
    expect((container.querySelector('#to') as HTMLInputElement).value).toBe('replyto@example.com');
    expect((container.querySelector('#subject') as HTMLInputElement).value).toBe('RE: Hello there');

    // The HTML editor carries the quoted original underneath the separator.
    const editor = container.querySelector('.rsw-ce')!;
    expect(editor.innerHTML).toContain('emissary-quote-separator');
    expect(editor.textContent).toContain('Original text');
  });

  it('quotes the original in the plain-text body with the exact separator layout', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail());
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/orig-id']}>
          <Routes>
            <Route path="/compose/:id" element={<Compose mode="reply" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();
    click(buttonByText(container, 'Plain text'));

    const body = container.querySelector('textarea#body') as HTMLTextAreaElement;
    // Six blank lines above the separator, one below it.
    expect(body.value).toBe('\n'.repeat(7) + '-----' + '\n\n' + 'Original text');
  });

  it('prefills a forward with no recipients and a FWD: subject', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail());
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/forward/orig-id']}>
          <Routes>
            <Route path="/compose/forward/:id" element={<Compose mode="forward" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();

    expect((container.querySelector('#to') as HTMLInputElement).value).toBe('');
    expect((container.querySelector('#subject') as HTMLInputElement).value).toBe('FWD: Hello there');
    const editor = container.querySelector('.rsw-ce')!;
    expect(editor.innerHTML).toContain('emissary-quote-separator');
    expect(editor.textContent).toContain('Original text');
  });

  it('falls back to the original sender when there is no reply-to', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail({ replyTo: undefined }));
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/orig-id']}>
          <Routes>
            <Route path="/compose/:id" element={<Compose mode="reply" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();
    expect((container.querySelector('#to') as HTMLInputElement).value).toBe('alice@example.com');
  });

  it('does not double the RE: prefix when the subject already has one', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail({ subject: 'Re: already prefixed' }));
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/orig-id']}>
          <Routes>
            <Route path="/compose/:id" element={<Compose mode="reply" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();
    expect((container.querySelector('#subject') as HTMLInputElement).value).toBe('Re: already prefixed');
  });

  it('does not double the FWD: prefix when the subject already has one', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail({ subject: 'fwd: chain' }));
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/forward/orig-id']}>
          <Routes>
            <Route path="/compose/forward/:id" element={<Compose mode="forward" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();
    expect((container.querySelector('#subject') as HTMLInputElement).value).toBe('fwd: chain');
  });
});

describe('Compose recipients and thread headers', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root!.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('rejects a message with no recipients', () => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose currentUser="me@example.com" />
        </MemoryRouter>,
      );
    });
    act(() => {
      container.querySelector('form')!.dispatchEvent(
        new Event('submit', { bubbles: true, cancelable: true }),
      );
    });
    expect(sendEmail).not.toHaveBeenCalled();
    expect(container.textContent).toContain('Please enter at least one recipient');
  });

  it('sends multiple To, Cc and Bcc recipients plus the thread headers', async () => {
    vi.mocked(getEmail).mockResolvedValueOnce(originalEmail());
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/compose/orig-id']}>
          <Routes>
            <Route path="/compose/:id" element={<Compose mode="reply" currentUser="me@example.com" />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();

    inputValue('to', 'a@example.com, b@example.com');
    click(buttonByText(container, 'Add Cc/Bcc'));
    inputValue('cc', 'c@example.com');
    inputValue('bcc', 'd@example.com');

    act(() => {
      container.querySelector('form')!.dispatchEvent(
        new Event('submit', { bubbles: true, cancelable: true }),
      );
    });
    await flush();

    expect(sendEmail).toHaveBeenCalledTimes(1);
    expect(sendEmail).toHaveBeenCalledWith(
      expect.objectContaining({
        to: 'a@example.com, b@example.com',
        cc: 'c@example.com',
        bcc: 'd@example.com',
        subject: 'RE: Hello there',
        inReplyTo: ['<orig@example.com>'],
        references: ['<root@example.com>', '<orig@example.com>'],
      }),
    );
  });

  it('reveals and hides the Cc/Bcc fields', () => {
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose currentUser="me@example.com" />
        </MemoryRouter>,
      );
    });
    expect(container.querySelector('#cc')).toBeNull();
    expect(container.querySelector('#bcc')).toBeNull();

    click(buttonByText(container, 'Add Cc/Bcc'));
    expect(container.querySelector('#cc')).not.toBeNull();
    expect(container.querySelector('#bcc')).not.toBeNull();

    click(buttonByText(container, 'Hide Cc/Bcc'));
    expect(container.querySelector('#cc')).toBeNull();
  });
});

describe('Compose body format', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose currentUser="me@example.com" />
        </MemoryRouter>,
      );
    });
  });

  afterEach(() => {
    act(() => root!.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('defaults to the HTML editor with a minimal formatting toolbar', () => {
    expect(container.querySelector('.rsw-ce')).not.toBeNull();
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelectorAll('.rsw-toolbar .rsw-btn').length).toBe(6);
    expect(buttonByText(container, 'HTML').getAttribute('aria-pressed')).toBe('true');
  });

  it('switches between plain text and HTML without losing either draft', () => {
    click(buttonByText(container, 'Plain text'));
    expect(container.querySelector('textarea#body')).not.toBeNull();
    expect(container.querySelector('.rsw-ce')).toBeNull();
    expect(buttonByText(container, 'Plain text').getAttribute('aria-pressed')).toBe('true');

    click(buttonByText(container, 'HTML'));
    expect(container.querySelector('textarea#body')).toBeNull();
    expect(container.querySelector('.rsw-ce')).not.toBeNull();
  });

  it('rejects an empty message body', () => {
    inputValue('to', 'someone@example.com');
    act(() => {
      container.querySelector('form')!.dispatchEvent(
        new Event('submit', { bubbles: true, cancelable: true }),
      );
    });
    expect(sendEmail).not.toHaveBeenCalled();
    expect(container.textContent).toContain('Please enter a message body');
  });
});
