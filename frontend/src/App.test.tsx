import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import App from './App';

const TOKEN_KEY = 'emissary_token';
const REFRESH_TOKEN_KEY = 'emissary_refresh_token';

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

function createMockResponse(data: unknown, options: { status?: number; ok?: boolean } = {}) {
  const status = options.status ?? 200;
  const ok = options.ok ?? (status >= 200 && status < 300);
  const jsonData = typeof data === 'string' ? data : JSON.stringify(data);
  return {
    status,
    ok,
    json: async () => data,
    text: async () => jsonData,
    headers: new Headers({ 'content-type': 'application/json' }),
  };
}

function makeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: unknown) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(payload)}.signature`;
}

describe('App routing', () => {
  let root: Root | null = null;

  const renderApp = () => {
    const container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root!.render(<App />);
    });
  };

  beforeEach(() => {
    window.localStorage.clear();
    window.history.replaceState(null, '', '/');
    document.body.replaceChildren();
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it('renders the inbox and requests emails via JMAP once authenticated', async () => {
    window.localStorage.setItem(TOKEN_KEY, makeJwt({ sub: 'test@example.com', exp: 9999999999 }));
    window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-token');

    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/.well-known/jmap') {
        return createMockResponse({
          username: 'test@example.com',
          apiUrl: '/api/',
          primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs' },
        });
      }
      if (url === '/api/') {
        return createMockResponse({
          methodResponses: [
            ['Email/query', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', queryState: '1', ids: ['e1'], total: 1 }, 'query1'],
            ['Email/get', { accountId: 'acct_lz3-Rj7IV4X1-Vr1ujkG7tstkxwk5pgkqJ6mXbpOgTs', state: '1', list: [jmapEmail], notFound: [] }, 'get1'],
          ],
          sessionState: '1',
        });
      }
      return createMockResponse({});
    });
    vi.stubGlobal('fetch', fetchMock);

    renderApp();
    // First flush resolves the local auth check, the second lets the
    // inbox effect and its JMAP requests complete.
    await act(async () => {});
    await act(async () => {});

    // Should have fetched the JMAP session, then the JMAP API
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock).toHaveBeenCalledWith('/.well-known/jmap', expect.anything());
    expect(fetchMock).toHaveBeenCalledWith('/api/', expect.anything());
    expect(document.body.textContent).toContain('Welcome to Emissary');
  });
});