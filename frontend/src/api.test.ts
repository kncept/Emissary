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

  describe('getRawEmail', () => {
    it('requests the raw message with the stored token and returns its text', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async () => createMockResponse('From: a@example.com\r\n\r\nBody\r\n', {
        status: 200,
        ok: true,
      }));
      vi.stubGlobal('fetch', fetchMock);

      const result = await getRawEmail('users/alice@example.com/inbox/e1.eml');

      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [url, options] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
      expect(url).toBe('/api/email/raw?id=users%2Falice%40example.com%2Finbox%2Fe1.eml');
      const headers = options.headers as Headers;
      expect(headers.get('Authorization')).toBe('Bearer abc123');
      expect(result).toBe('From: a@example.com\r\n\r\nBody\r\n');
    });

    it('surfaces the server error message', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      vi.stubGlobal('fetch', vi.fn(async () => createMockResponse({ error: 'email not found' }, {
        status: 404,
        ok: false,
      })));

      await expect(getRawEmail('missing.eml')).rejects.toThrow('email not found');
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

  describe('sendEmail body formats', () => {
    function stubJmapFetch() {
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
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
              ['Email/set', { accountId: 'test@example.com', created: { create1: { id: 'draft1' } }, notCreated: {} }, 'emailCreate'],
              ['EmailSubmission/set', { accountId: 'test@example.com', created: { submission1: { id: 'sub1' } }, notCreated: {} }, 'submissionCreate'],
            ],
            sessionState: '1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);
      return fetchMock;
    }

    async function sentJmapBody(fetchMock: ReturnType<typeof stubJmapFetch>) {
      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/');
      return JSON.parse(String(apiCall?.[1]?.body));
    }

    async function sentCreateArgs(fetchMock: ReturnType<typeof stubJmapFetch>) {
      const body = await sentJmapBody(fetchMock);
      return body.methodCalls[0][1].create.create1;
    }

    async function sentSubmissionArgs(fetchMock: ReturnType<typeof stubJmapFetch>) {
      const body = await sentJmapBody(fetchMock);
      return body.methodCalls[1][1];
    }

    beforeEach(() => {
      window.localStorage.clear();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
    });

    it('sends a text/plain-only message in plain mode', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch();

      await sendEmail({
        to: 'bob@example.com',
        subject: 'Hi',
        body: 'Hello <b>world</b>',
        format: 'plain',
      });

      const create = await sentCreateArgs(fetchMock);
      expect(create.textBody).toEqual([{ partId: 'text1', type: 'text/plain', size: 18 }]);
      expect(create.bodyValues.text1.value).toBe('Hello <b>world</b>');
      expect(create.htmlBody).toBeUndefined();
      expect(create.bodyValues.html1).toBeUndefined();
    });

    it('sends an HTML message with a plain alternative in HTML mode', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch();

      await sendEmail({
        to: 'bob@example.com',
        subject: 'Hi',
        body: '<p>Hello <b>world</b></p>',
        format: 'html',
      });

      const create = await sentCreateArgs(fetchMock);
      expect(create.htmlBody).toEqual([{ partId: 'html1', type: 'text/html', size: 25 }]);
      expect(create.bodyValues.html1.value).toBe('<p>Hello <b>world</b></p>');
      expect(create.bodyValues.text1.value).toBe('Hello world');
    });

    it('never sends a From and expands comma-separated recipients', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch();

      await sendEmail({
        to: 'bob@example.com, carol@example.com',
        cc: ' dave@example.com ,',
        bcc: 'erin@example.com',
        subject: 'Hi',
        body: 'Hello',
        format: 'plain',
      });

      const create = await sentCreateArgs(fetchMock);
      expect(create.from).toBeUndefined();
      expect(create.to).toEqual([
        { email: 'bob@example.com', name: '' },
        { email: 'carol@example.com', name: '' },
      ]);
      expect(create.cc).toEqual([{ email: 'dave@example.com', name: '' }]);
      expect(create.bcc).toEqual([{ email: 'erin@example.com', name: '' }]);

      // The envelope covers every recipient, including Bcc.
      const submission = await sentSubmissionArgs(fetchMock);
      expect(submission.create.submission1.envelope.rcptTo).toEqual([
        { email: 'bob@example.com' },
        { email: 'carol@example.com' },
        { email: 'dave@example.com' },
        { email: 'erin@example.com' },
      ]);
      expect(submission.create.submission1.envelope.mailFrom).toBeUndefined();
    });

    it('carries the reply/forward thread headers into the draft', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch();

      await sendEmail({
        to: 'alice@example.com',
        subject: 'RE: Hi',
        body: 'Replying',
        format: 'plain',
        inReplyTo: ['<orig@example.com>'],
        references: ['<root@example.com>', '<orig@example.com>'],
      });

      const create = await sentCreateArgs(fetchMock);
      expect(create.inReplyTo).toEqual(['<orig@example.com>']);
      expect(create.references).toEqual(['<root@example.com>', '<orig@example.com>']);
    });

    it('omits thread headers and cc/bcc when not supplied', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch();

      await sendEmail({
        to: 'bob@example.com',
        subject: 'Hi',
        body: 'Hello',
        format: 'plain',
      });

      const create = await sentCreateArgs(fetchMock);
      expect(create.inReplyTo).toBeUndefined();
      expect(create.references).toBeUndefined();
      expect(create.cc).toBeUndefined();
      expect(create.bcc).toBeUndefined();
    });
  });

  describe('parseAddressList', () => {
    it('splits, trims and drops empty entries', async () => {
      const { parseAddressList } = await importApi();
      expect(parseAddressList('a@example.com, b@example.com')).toEqual([
        'a@example.com',
        'b@example.com',
      ]);
      expect(parseAddressList(' a@example.com ,, c@example.com ,')).toEqual([
        'a@example.com',
        'c@example.com',
      ]);
      expect(parseAddressList('')).toEqual([]);
    });
  });

  describe('token refresh on 401', () => {
    const refreshedTokens = {
      accessToken: 'new-token',
      refreshToken: 'refresh-2',
      tokenType: 'Bearer',
      expiresAt: '2099-01-01T00:00:00Z',
    };

    const jmapSession = {
      username: 'test@example.com',
      apiUrl: '/api/',
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'test@example.com' },
    };

    const authHeadersFor = (fetchMock: ReturnType<typeof vi.fn>, url: string) =>
      fetchMock.mock.calls
        .filter((call) => String(call[0]) === url)
        .map((call) => {
          const options = call[1];
          return options ? (options.headers as Headers).get('Authorization') : null;
        });

    it('refreshes and retries a REST request with the new access token', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'old-token');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1');
      let rawAttempt = 0;
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/api/auth/refresh') return createMockResponse(refreshedTokens);
        if (url.startsWith('/api/email/raw')) {
          rawAttempt += 1;
          return rawAttempt === 1
            ? createMockResponse({ error: 'unauthorized' }, { status: 401, ok: false })
            : createMockResponse('From: alice@example.com\r\n\r\nBody');
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await getRawEmail('e1.eml');

      expect(result).toContain('Body');
      expect(fetchMock.mock.calls.filter((call) => String(call[0]) === '/api/auth/refresh')).toHaveLength(1);
      expect(authHeadersFor(fetchMock, '/api/email/raw?id=e1.eml')).toEqual([
        'Bearer old-token',
        'Bearer new-token',
      ]);
      expect(window.localStorage.getItem(TOKEN_KEY)).toBe('new-token');
      expect(window.localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-2');
    });

    it('refreshes and retries a JMAP session request with the new access token', async () => {
      const { getEmails } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'old-token');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1');
      let sessionAttempt = 0;
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/api/auth/refresh') return createMockResponse(refreshedTokens);
        if (url === '/.well-known/jmap') {
          sessionAttempt += 1;
          return sessionAttempt === 1
            ? createMockResponse({ error: 'unauthorized' }, { status: 401, ok: false })
            : createMockResponse(jmapSession);
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

      await expect(getEmails()).resolves.toEqual([]);

      expect(fetchMock.mock.calls.filter((call) => String(call[0]) === '/api/auth/refresh')).toHaveLength(1);
      expect(authHeadersFor(fetchMock, '/.well-known/jmap')).toEqual([
        'Bearer old-token',
        'Bearer new-token',
      ]);
      expect(authHeadersFor(fetchMock, '/api/')).toEqual(['Bearer new-token']);
    });

    it('shares one refresh across concurrent JMAP requests', async () => {
      const { getEmails } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'old-token');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1');
      let postAttempt = 0;
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/api/auth/refresh') return createMockResponse(refreshedTokens);
        if (url === '/.well-known/jmap') return createMockResponse(jmapSession);
        if (url === '/api/') {
          postAttempt += 1;
          return postAttempt <= 2
            ? createMockResponse({ error: 'unauthorized' }, { status: 401, ok: false })
            : createMockResponse({
              methodResponses: [['Email/get', { list: [] }, 'get1']],
              sessionState: '1',
            });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await Promise.all([getEmails(), getEmails()]);

      expect(fetchMock.mock.calls.filter((call) => String(call[0]) === '/api/auth/refresh')).toHaveLength(1);
      expect(authHeadersFor(fetchMock, '/api/')).toEqual([
        'Bearer old-token',
        'Bearer old-token',
        'Bearer new-token',
        'Bearer new-token',
      ]);
    });

    it('clears the tokens when the refresh itself fails', async () => {
      const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      try {
        const { getEmails } = await importApi();
        window.localStorage.setItem(TOKEN_KEY, 'old-token');
        window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1');
        vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
          const url = String(input);
          if (url === '/.well-known/jmap') {
            return createMockResponse({ error: 'unauthorized' }, { status: 401, ok: false });
          }
          if (url === '/api/auth/refresh') {
            return createMockResponse({ error: 'invalid refresh token' }, { status: 401, ok: false });
          }
          throw new Error(`Unexpected fetch: ${url}`);
        }));

        await expect(getEmails()).rejects.toThrow('Unauthorized');

        expect(window.localStorage.getItem(TOKEN_KEY)).toBeNull();
        expect(window.localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      } finally {
        consoleSpy.mockRestore();
      }
    });
  });
});