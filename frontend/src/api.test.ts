import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { checkAuth, deleteEmail, login } from './api';

const TOKEN_KEY = 'emissary_token';
const REFRESH_TOKEN_KEY = 'emissary_refresh_token';

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

describe('auth api', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('checkAuth', () => {
    it('returns unauthenticated immediately when no token is stored', async () => {
      const fetchMock = vi.fn();
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).not.toHaveBeenCalled();
      expect(result).toEqual({ authenticated: false });
    });

    it('sends the stored token as a Bearer header', async () => {
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/api/auth/check') {
          return createMockResponse({ authenticated: true, username: 'Emissary', admin: false });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).toHaveBeenCalledTimes(1);
      const call = fetchMock.mock.calls[0] as [string, RequestInit?];
      expect(call[0]).toBe('/api/auth/check');
      const headers = call[1]?.headers as Headers | Record<string, string> | string[][] | undefined;
      let authHeader: string | null = null;
      if (headers instanceof Headers) {
        authHeader = headers.get('Authorization');
      } else if (Array.isArray(headers)) {
        authHeader = headers.find(([k]) => k.toLowerCase() === 'authorization')?.[1] ?? null;
      } else if (headers) {
        authHeader = headers['Authorization'] ?? headers['authorization'] ?? null;
      }
      expect(authHeader).toBe('Bearer abc123');
      expect(result).toEqual({ authenticated: true, username: 'Emissary', admin: false });
    });
  });

  describe('deleteEmail', () => {
    it('sends a JMAP Email/set destroy request with the stored token', async () => {
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
        if (url === '/api') {
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

      // Should have called session, then JMAP API
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(fetchMock).toHaveBeenCalledWith('/.well-known/jmap', expect.any(Object));
      const apiCall = fetchMock.mock.calls[1] as [string, RequestInit?];
      expect(apiCall[0]).toBe('/api');
      const apiOptions = apiCall[1];
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
        if (url === '/api') {
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
  });
});