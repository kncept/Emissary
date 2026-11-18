import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const TOKEN_KEY = 'emissary_token';
const REFRESH_TOKEN_KEY = 'emissary_refresh_token';
const MASQUERADE_KEY = 'masquerade_as';

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

async function importApi() {
  vi.resetModules();
  return await import('./api');
}

function makeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: unknown) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(payload)}.signature`;
}

describe('auth api', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('checkAuth', () => {
    it('returns unauthenticated immediately when no token is stored', async () => {
      const { checkAuth } = await importApi();
      const fetchMock = vi.fn();
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).not.toHaveBeenCalled();
      expect(result).toEqual({ authenticated: false });
    });

    it('reads the username and role claims from the stored token without calling the API', async () => {
      const { checkAuth } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, makeJwt({
        sub: 'Emissary',
        SystemManager: true,
        EmissaryUserManagement: true,
        exp: 9999999999,
      }));
      const fetchMock = vi.fn();
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).not.toHaveBeenCalled();
      expect(result).toEqual({
        authenticated: true,
        username: 'Emissary',
        systemManager: true,
        emissaryUserManagement: true,
      });
    });

    it('returns unauthenticated when the stored token is not a parseable JWT', async () => {
      const { checkAuth } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'not-a-jwt');
      const fetchMock = vi.fn();
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).not.toHaveBeenCalled();
      expect(result).toEqual({ authenticated: false });
    });
  });

  describe('deleteEmail', () => {
    it('sends a JMAP Email/set destroy request with the stored token', async () => {
      const { deleteEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-token');

      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'test@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [
              ['Email/set', { accountId: 'test@example.com', oldState: '1', newState: '2', created: {}, updated: {}, destroyed: ['email-id'], notDestroyed: {} }, 'set1'],
            ],
            sessionState: '2',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await deleteEmail('email-id');

      // deleteEmail calls jmapRequest which fetches session then makes JMAP API call
      expect(fetchMock).toHaveBeenCalledWith('/.well-known/jmap', expect.any(Object));
      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/');
      expect(apiCall).toBeDefined();
      const apiOptions = (apiCall as unknown as [string, RequestInit?])[1];
      expect(apiOptions).toBeDefined();
      expect(apiOptions!.method).toBe('POST');
      // Headers can be Headers object, string[][], or Record<string, string>
      const headers = apiOptions!.headers as Record<string, string> | Headers | string[][];
      let authHeader: string | null = null;
      if (headers instanceof Headers) {
        authHeader = headers.get('Authorization');
      } else if (Array.isArray(headers)) {
        authHeader = headers.find(([k]) => k.toLowerCase() === 'authorization')?.[1] ?? null;
      } else {
        authHeader = headers['Authorization'] ?? headers['authorization'] ?? null;
      }
      expect(authHeader).toBe('Bearer abc123');
      const body = JSON.parse(apiOptions!.body as string);
      expect(body.methodCalls[0][0]).toBe('Email/set');
      expect(body.methodCalls[0][1].destroy).toContain('email-id');
    });

    it('throws when the delete fails', async () => {
      const { deleteEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-token');

      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'test@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [
              ['error', { type: 'notFound', description: 'Email not found' }, 'set1'],
            ],
            sessionState: '2',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await expect(deleteEmail('missing')).rejects.toThrow('Failed to delete email');
    });
  });

  describe('login', () => {
    it('stores the returned tokens and reports success to the caller', async () => {
      const { login } = await importApi();
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/api/auth/login') {
          return createMockResponse({
            accessToken: 'jwt-access-token',
            refreshToken: 'jwt-refresh-token',
            tokenType: 'Bearer',
            expiresAt: '2026-01-01T00:00:00Z',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await login('user', 'pass');

      expect(result.token).toBe('jwt-access-token');
      expect(window.localStorage.getItem(TOKEN_KEY)).toBe('jwt-access-token');
      expect(window.localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('jwt-refresh-token');
    });

    it('does not store tokens and surfaces the error on failure', async () => {
      const { login } = await importApi();
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/api/auth/login') {
          return createMockResponse({ error: 'invalid credentials' }, { status: 401, ok: false });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await login('user', 'wrong');

      expect(result.token).toBeUndefined();
      expect(result.error).toBe('invalid credentials');
      expect(window.localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(window.localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    });

    it('clears a leftover masquerade target on successful login', async () => {
      const { login } = await importApi();
      window.localStorage.setItem(MASQUERADE_KEY, 'other@example.com');
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
        if (String(input) === '/api/auth/login') {
          return createMockResponse({
            accessToken: makeJwt({ sub: 'admin@example.com' }),
            refreshToken: 'refresh-token',
            tokenType: 'Bearer',
            expiresAt: '9999999999',
          });
        }
        throw new Error(`Unexpected fetch: ${String(input)}`);
      }));

      const result = await login('admin@example.com', 'password');

      expect(result.token).toBeDefined();
      expect(window.localStorage.getItem(MASQUERADE_KEY)).toBeNull();
    });

    it('requests emails for the logged-in account, not a stale masquerade target', async () => {
      const { login, getEmails } = await importApi();
      window.localStorage.setItem(MASQUERADE_KEY, 'other@example.com');
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/api/auth/login') {
          return createMockResponse({
            accessToken: makeJwt({ sub: 'admin@example.com' }),
            refreshToken: 'refresh-token',
            tokenType: 'Bearer',
            expiresAt: '9999999999',
          });
        }
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'admin@example.com',
            apiUrl: '/api/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'admin@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [['Email/get', { list: [] }, 'get1']],
            sessionState: '1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await login('admin@example.com', 'password');
      await getEmails();

      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/');
      expect(apiCall).toBeDefined();
      const body = JSON.parse(String(apiCall?.[1]?.body));
      expect(body.methodCalls[0][1].accountId).toBe('admin@example.com');
    });
  });
});