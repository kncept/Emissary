import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getEmails, login, logout } from '../api.ts';
import { resetJMAPClientCache } from '../api/jmap-client.ts';

const jmapEmailA = {
  id: 'e1',
  from: [{ email: 'sender@example.com' }],
  to: [{ email: 'usera@example.com' }],
  subject: 'Email for User A',
  preview: 'Hello User A',
  receivedAt: '2026-11-20T00:00:00Z',
  keywords: {},
  mailboxIds: { inbox: true },
};

const jmapEmailB = {
  id: 'e2',
  from: [{ email: 'sender@example.com' }],
  to: [{ email: 'userb@example.com' }],
  subject: 'Email for User B',
  preview: 'Hello User B',
  receivedAt: '2026-11-20T00:00:00Z',
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

describe('JMAP client cache identity binding (F-C1)', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let fetchMock: ReturnType<typeof vi.fn>;

  const flush = async (ms = 0) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-20T12:00:00Z'));
    window.localStorage.clear();
    resetJMAPClientCache();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === '/api/auth/login') {
        const body = JSON.parse(String(init?.body ?? '{}')) as { username: string; password: string };
        if (body.username === 'usera@example.com') {
          // Return token for user A with sid
          const payload = { sub: 'usera@example.com', sid: 'session-a', systemManager: false, emissaryUserManagement: false };
          const token = `header.${btoa(JSON.stringify(payload))}.signature`;
          return createMockResponse({
            accessToken: token,
            refreshToken: 'refresh-a',
            tokenType: 'Bearer',
            expiresAt: Date.now() + 3600000,
          });
        }
        if (body.username === 'userb@example.com') {
          // Return token for user B with different sid
          const payload = { sub: 'userb@example.com', sid: 'session-b', systemManager: false, emissaryUserManagement: false };
          const token = `header.${btoa(JSON.stringify(payload))}.signature`;
          return createMockResponse({
            accessToken: token,
            refreshToken: 'refresh-b',
            tokenType: 'Bearer',
            expiresAt: Date.now() + 3600000,
          });
        }
        return { status: 401, ok: false, json: async () => ({ error: 'Invalid credentials' }) };
      }
      if (url === '/api/auth/logout') {
        return createMockResponse({});
      }
      if (url === '/.well-known/jmap') {
        // Return different sessions based on the Authorization header
        const authHeader = init?.headers && new Headers(init.headers).get('Authorization');
        const token = authHeader?.replace('Bearer ', '');
        let username = 'usera@example.com';
        if (token) {
          try {
            const payload = JSON.parse(atob(token.split('.')[1]));
            username = payload.sub || 'usera@example.com';
          } catch {
            // ignore
          }
        }
        if (username === 'userb@example.com') {
          return createMockResponse({
            username: 'userb@example.com',
            apiUrl: '/api/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_user_b' },
          });
        }
        return createMockResponse({
          username: 'usera@example.com',
          apiUrl: '/api/',
          primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_user_a' },
        });
      }
      if (url === '/api/') {
        const request = JSON.parse(String(init?.body ?? '{}')) as { methodCalls?: string[][] };
        if (request.methodCalls?.some((call) => call[0] === 'Mailbox/query')) {
          return createMockResponse({
            methodResponses: [
              ['Mailbox/query', { accountId: 'acct_user_a', state: 'm1', ids: ['inbox-id-a'] }, 'mailboxQuery'],
              ['Mailbox/get', { accountId: 'acct_user_a', state: 'm1', list: [{ id: 'inbox-id-a', role: 'inbox', name: 'Inbox' }], notFound: [] }, 'mailboxGet'],
            ],
            sessionState: '1',
          });
        }
        // Return emails based on the current account
        const authHeader = init?.headers && new Headers(init.headers).get('Authorization');
        const token = authHeader?.replace('Bearer ', '');
        let username = 'usera@example.com';
        if (token) {
          try {
            const payload = JSON.parse(atob(token.split('.')[1]));
            username = payload.sub || 'usera@example.com';
          } catch {
            // ignore
          }
        }
        if (username === 'userb@example.com') {
          return createMockResponse({
            methodResponses: [
              ['Email/query', { accountId: 'acct_user_b', queryState: '1', ids: ['e2'], total: 1 }, 'query1'],
              ['Email/get', { accountId: 'acct_user_b', state: '1', list: [jmapEmailB], notFound: [] }, 'get1'],
            ],
            sessionState: '1',
          });
        }
        return createMockResponse({
          methodResponses: [
            ['Email/query', { accountId: 'acct_user_a', queryState: '1', ids: ['e1'], total: 1 }, 'query1'],
            ['Email/get', { accountId: 'acct_user_a', state: '1', list: [jmapEmailA], notFound: [] }, 'get1'],
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

  it('uses User B account after logout/login as different user', async () => {
    // Step 1: Login as User A using the real login function
    const resultA = await login('usera@example.com', 'password');
    expect(resultA.token).toBeDefined();
    expect(resultA.username).toBe('usera@example.com');

    // Step 2: Fetch emails as User A (this populates the JMAP cache with User A's session)
    const emailsA = await getEmails();
    expect(emailsA).toHaveLength(1);
    expect(emailsA[0].subject).toBe('Email for User A');

    // Step 3: Logout using the real logout function
    await logout();

    // Step 4: Login as User B using the real login function
    const resultB = await login('userb@example.com', 'password');
    expect(resultB.token).toBeDefined();
    expect(resultB.username).toBe('userb@example.com');

    // Step 5: Fetch emails as User B - this should use User B's account, not User A's cached session
    const emailsB = await getEmails();
    
    // THIS ASSERTION SHOULD PASS AFTER THE FIX:
    // The JMAP client cache is now keyed by token sub+sid
    expect(emailsB).toHaveLength(1);
    expect(emailsB[0].subject).toBe('Email for User B');
  });
});