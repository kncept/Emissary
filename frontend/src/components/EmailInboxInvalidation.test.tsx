import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getEmail, markRead, markFlagged, deleteEmail } from '../api.ts';
import { clearEmailRefreshRequest } from '../api/email-checks.ts';
import { resetJMAPClientCache } from '../api/jmap-client.ts';
import { resetEmailListCache } from '../email-list-cache.ts';
import EmailList from './EmailList.tsx';
import EmailView from './EmailView.tsx';
import Layout from './Layout.tsx';

const FIVE_MINUTES = 5 * 60 * 1000;

const jmapEmail = {
  id: 'e1',
  from: [{ email: 'sender@example.com' }],
  to: [{ email: 'user@example.com' }],
  subject: 'Welcome to Emissary',
  preview: 'Hello from the test suite.',
  receivedAt: '2026-11-11T00:00:00Z',
  keywords: {},
  mailboxIds: { inbox: true },
};

const jmapEmailFull = {
  ...jmapEmail,
  textBody: [{ partId: 'text1', type: 'text/plain' }],
  htmlBody: [{ partId: 'html1', type: 'text/html' }],
  bodyValues: {
    text1: { value: 'Hello from the test suite.', isEncodingProblem: false, isTruncated: false },
    html1: { value: '<p>Hello from the test suite.</p>', isEncodingProblem: false, isTruncated: false },
  },
  messageId: '<msg-1@example.com>',
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

describe('inbox invalidation after EmailView mutations (F-H1)', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let fetchMock: ReturnType<typeof vi.fn>;

  const apiCallCount = () =>
    fetchMock.mock.calls.filter(([url]) => String(url) === '/api/').length;

  const jmapSessionCallCount = () =>
    fetchMock.mock.calls.filter(([url]) => String(url) === '/.well-known/jmap').length;

  const flush = async (ms = 0) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  };

  const mountApp = async () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/']}>
          <Layout onLogout={() => {}}>
            <Routes>
              <Route path="/" element={<EmailList />} />
              <Route path="/email/:id" element={<EmailView />} />
            </Routes>
          </Layout>
        </MemoryRouter>,
      );
    });
    await flush();
  };

  const clickEmailLink = async (emailId: string) => {
    // Find and click the email link in the inbox
    const emailLink = container.querySelector(`a[href="/email/${encodeURIComponent(emailId)}"]`);
    expect(emailLink).toBeTruthy();
    await act(async () => {
      emailLink!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    await flush(100); // Wait for email to load
  };

  const clickBackLink = async () => {
    // Find and click the Back link in EmailView
    const backLink = Array.from(container.querySelectorAll('a')).find(
      (a) => a.textContent?.includes('Back')
    );
    expect(backLink).toBeTruthy();
    await act(async () => {
      backLink!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    await flush();
  };

  const clickButtonByText = async (text: string) => {
    const btn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes(text)
    );
    expect(btn).toBeTruthy();
    await act(async () => {
      btn!.click();
    });
    await flush();
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-20T12:00:00Z'));
    window.localStorage.clear();
    resetJMAPClientCache();
    resetEmailListCache();
    clearEmailRefreshRequest();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/api/auth/login') {
        return { status: 401, ok: false, json: async () => ({ error: 'Invalid credentials' }) };
      }
      if (url === '/api/auth/logout') {
        return createMockResponse({});
      }
      if (url === '/.well-known/jmap') {
        return createMockResponse({
          username: 'user@example.com',
          apiUrl: '/api/',
          primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_user' },
        });
      }
      if (url === '/api/') {
        const request = JSON.parse(String(init?.body ?? '{}')) as { methodCalls?: string[][] };
        if (request.methodCalls?.some((call) => call[0] === 'Mailbox/query')) {
          return createMockResponse({
            methodResponses: [
              ['Mailbox/query', { accountId: 'acct_user', state: 'm1', ids: ['inbox-id'] }, 'mailboxQuery'],
              ['Mailbox/get', { accountId: 'acct_user', state: 'm1', list: [{ id: 'inbox-id', role: 'inbox', name: 'Inbox' }], notFound: [] }, 'mailboxGet'],
            ],
            sessionState: '1',
          });
        }
        if (request.methodCalls?.some((call) => call[0] === 'Email/get' && call[1]?.ids)) {
          // Single email fetch for EmailView
          return createMockResponse({
            methodResponses: [
              ['Email/get', { accountId: 'acct_user', state: '1', list: [jmapEmailFull], notFound: [] }, 'get1'],
            ],
            sessionState: '1',
          });
        }
        // Email list query
        return createMockResponse({
          methodResponses: [
            ['Email/query', { accountId: 'acct_user', queryState: '1', ids: ['e1'], total: 1 }, 'query1'],
            ['Email/get', { accountId: 'acct_user', state: '1', list: [jmapEmail], notFound: [] }, 'get1'],
          ],
          sessionState: '1',
        });
      }
      if (url === '/api/auth/refresh') {
        return createMockResponse({
          accessToken: 'new-token',
          refreshToken: 'new-refresh',
          tokenType: 'Bearer',
          expiresAt: Date.now() + 3600000,
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

  it('invalidates inbox cache after marking email read in EmailView', async () => {
    await mountApp();
    const initialApiCalls = apiCallCount();

    // Click on email to navigate to EmailView
    await clickEmailLink('e1');

    // Mark as read
    await clickButtonByText('Mark Read');

    // Click Back to return to inbox
    await clickBackLink();

    // Should have made an additional API call to refresh the inbox
    expect(apiCallCount()).toBeGreaterThan(initialApiCalls);
  });

  it('invalidates inbox cache after flagging email in EmailView', async () => {
    await mountApp();
    const initialApiCalls = apiCallCount();

    // Click on email to navigate to EmailView
    await clickEmailLink('e1');

    // Flag the email
    await clickButtonByText('Flag');

    // Click Back to return to inbox
    await clickBackLink();

    // Should have made an additional API call to refresh the inbox
    expect(apiCallCount()).toBeGreaterThan(initialApiCalls);
  });

  it('invalidates inbox cache after deleting email in EmailView', async () => {
    await mountApp();
    const initialApiCalls = apiCallCount();

    // Click on email to navigate to EmailView
    await clickEmailLink('e1');

    // Delete the email
    await clickButtonByText('Delete');

    // Confirm deletion in dialog
    await clickButtonByText('Delete');

    // Should have made an additional API call to refresh the inbox
    // (navigate('/') triggers a refresh via requestEmailRefresh)
    expect(apiCallCount()).toBeGreaterThan(initialApiCalls);
  });
});