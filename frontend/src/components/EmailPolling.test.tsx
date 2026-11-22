import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getEmails } from '../api.ts';
import { clearEmailRefreshRequest } from '../api/email-checks.ts';
import { resetJMAPClientCache } from '../api/jmap-client.ts';
import EmailList, { resetEmailListCache } from './EmailList.tsx';
import Layout from './Layout.tsx';

const FIVE_MINUTES = 5 * 60 * 1000;

const jmapEmail = {
  id: 'e1',
  from: [{ email: 'alice@example.com' }],
  to: [{ email: 'bob@example.com' }],
  subject: 'Welcome to Emissary',
  preview: 'Hello from the test suite.',
  receivedAt: '2026-11-11T00:00:00Z',
  keywords: {},
  mailboxIds: { inbox: true },
};

function createMockResponse(data: unknown) {
  return {
    status: 200,
    ok: true,
    json: async () => data,
    text: async () => JSON.stringify(data),
    headers: new Headers({ 'content-type': 'application/json' }),
  };
}

describe('inbox update checks', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let fetchMock: ReturnType<typeof vi.fn>;

  const apiCallCount = () =>
    fetchMock.mock.calls.filter(([url]) => String(url) === '/api/').length;

  const flush = async (ms = 0) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-20T12:00:00Z'));
    window.localStorage.clear();
    resetEmailListCache();
    resetJMAPClientCache();
    clearEmailRefreshRequest();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/.well-known/jmap') {
        return createMockResponse({
          username: 'test@example.com',
          apiUrl: '/api/',
          primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs' },
        });
      }
      if (url === '/api/') {
        const request = JSON.parse(String(init?.body ?? '{}')) as { methodCalls?: string[][] };
        if (request.methodCalls?.some((call) => call[0] === 'Mailbox/query')) {
          return createMockResponse({
            methodResponses: [
              ['Mailbox/query', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', state: 'm1', ids: ['inbox-id'] }, 'mailboxQuery'],
              ['Mailbox/get', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', state: 'm1', list: [{ id: 'inbox-id', role: 'inbox', name: 'Inbox' }], notFound: [] }, 'mailboxGet'],
            ],
            sessionState: '1',
          });
        }
        return createMockResponse({
          methodResponses: [
            ['Email/query', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', queryState: '1', ids: ['e1'], total: 1 }, 'query1'],
            ['Email/get', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', state: '1', list: [jmapEmail], notFound: [] }, 'get1'],
          ],
          sessionState: '1',
        });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const mountInbox = async () => {
    act(() => {
      root!.render(<MemoryRouter><EmailList /></MemoryRouter>);
    });
    await flush();
  };

  const mountInboxWithNav = async () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/']}>
          <Layout onLogout={() => {}}>
            <Routes>
              <Route path="/" element={<EmailList />} />
            </Routes>
          </Layout>
        </MemoryRouter>,
      );
    });
    await flush();
  };

  it('checks for updates five minutes after the last check while the inbox is open', async () => {
    await mountInbox();
    expect(apiCallCount()).toBe(2);

    await flush(FIVE_MINUTES - 1);
    expect(apiCallCount()).toBe(2);

    await flush(1);
    expect(apiCallCount()).toBe(3);

    await flush(FIVE_MINUTES);
    expect(apiCallCount()).toBe(4);
  });

  it('restarts the timer when emails are refreshed outside the inbox', async () => {
    await mountInbox();
    expect(apiCallCount()).toBe(2);

    await flush(FIVE_MINUTES - 1000);
    await act(async () => {
      await getEmails();
    });
    expect(apiCallCount()).toBe(3);

    await flush(FIVE_MINUTES - 1);
    expect(apiCallCount()).toBe(3);

    await flush(1);
    expect(apiCallCount()).toBe(4);
  });

  it('polls again when the Inbox nav item is clicked and restarts the timer', async () => {
    await mountInboxWithNav();
    expect(apiCallCount()).toBe(2);

    await flush(FIVE_MINUTES - 1000);
    const inboxLink = container.querySelector('a[href="/"]')!;
    expect(inboxLink.textContent).toContain('Inbox');
    await act(async () => {
      inboxLink.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(apiCallCount()).toBe(3);

    await flush(FIVE_MINUTES - 1);
    expect(apiCallCount()).toBe(3);

    await flush(1);
    expect(apiCallCount()).toBe(4);
  });

  it('stops checking once the inbox is no longer mounted', async () => {
    await mountInbox();
    expect(apiCallCount()).toBe(2);

    act(() => {
      root!.unmount();
    });
    root = null;

    await flush(FIVE_MINUTES * 3);
    expect(apiCallCount()).toBe(2);
  });
});
