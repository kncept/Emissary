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
    const session = {
      username: 'alice@example.com',
      apiUrl: '/api/jmap/',
      downloadUrl: '/download/{accountId}/{blobId}/{name}?type={type}',
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_alice' },
    };

    it('resolves blobId via Email/get and downloads from the Session template', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/.well-known/jmap') return createMockResponse(session);
        if (url === '/api/jmap/') {
          return createMockResponse({
            methodResponses: [[
              'Email/get',
              { accountId: 'acct_alice', list: [{ id: 'e1', blobId: 'blob-1' }] },
              'raw1',
            ]],
            sessionState: 's1',
          });
        }
        if (url === '/download/acct_alice/blob-1/message.eml?type=message%2Frfc822') {
          return createMockResponse('From: a@example.com\r\n\r\nBody\r\n');
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await getRawEmail('e1');

      expect(fetchMock).toHaveBeenCalledTimes(3);
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(urls).toEqual([
        '/.well-known/jmap',
        '/api/jmap/',
        '/download/acct_alice/blob-1/message.eml?type=message%2Frfc822',
      ]);
      for (const call of fetchMock.mock.calls) {
        const headers = (call[1] as RequestInit).headers as Headers;
        expect(headers.get('Authorization')).toBe('Bearer abc123');
      }
      expect(result).toBe('From: a@example.com\r\n\r\nBody\r\n');
    });

    it('reports a missing email without attempting a download', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/.well-known/jmap') return createMockResponse(session);
        if (url === '/api/jmap/') {
          return createMockResponse({
            methodResponses: [['Email/get', { accountId: 'acct_alice', list: [] }, 'raw1']],
            sessionState: 's1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await expect(getRawEmail('missing')).rejects.toThrow('email not found');
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    it('surfaces the blob download error message', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/.well-known/jmap') return createMockResponse(session);
        if (url === '/api/jmap/') {
          return createMockResponse({
            methodResponses: [[
              'Email/get',
              { accountId: 'acct_alice', list: [{ id: 'e1', blobId: 'blob-1' }] },
              'raw1',
            ]],
            sessionState: 's1',
          });
        }
        return createMockResponse({ detail: 'Blob not found' }, { status: 404, ok: false });
      });
      vi.stubGlobal('fetch', fetchMock);

      await expect(getRawEmail('e1')).rejects.toThrow('Blob not found');
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

  describe('masquerade account id', () => {
    it('converts a stored masquerade email into the opaque JMAP account id', async () => {
      const { getEmails } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      window.localStorage.setItem(MASQUERADE_KEY, 'Victim@Example.com');
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'admin@example.com',
            apiUrl: '/api/jmap/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_admin' },
          });
        }
        if (url === '/api/jmap/') {
          return createMockResponse({
            methodResponses: [['Email/get', { list: [] }, 'get1']],
            sessionState: 's1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await getEmails();

      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/jmap/');
      const body = JSON.parse(String(apiCall?.[1]?.body));
      expect(body.methodCalls[0][1].accountId).toBe('acct__76M_0-fjYsQlGD5dcND6ULNTD7RkTI-uDN0ri6k3l8');
    });
  });

  describe('sendEmail body formats', () => {
    function stubJmapFetch(
      identityId = 'discovered-identity',
      extraIdentities: { id: string; mayDelete?: boolean }[] = [],
    ) {
      const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'test@example.com' },
          });
        }
        if (url === '/api/') {
          const request = JSON.parse(String(init?.body ?? '{}')) as {
            methodCalls?: [string, Record<string, unknown>, string][];
          };
          if (request.methodCalls?.some((call) => call[0] === 'Identity/get')) {
            return createMockResponse({
              methodResponses: [[
                'Identity/get',
                {
                  accountId: 'test@example.com',
                  list: [
                    ...extraIdentities,
                    { id: identityId, email: 'test@example.com', mayDelete: false },
                  ],
                  notFound: [],
                },
                'identityGet',
              ]],
              sessionState: '1',
            });
          }
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
      const apiCall = fetchMock.mock.calls.find((call) => {
        if (call[0] !== '/api/') return false;
        const body = JSON.parse(String(call[1]?.body));
        return body.methodCalls.some((methodCall: [string]) => methodCall[0] === 'Email/set');
      });
      return JSON.parse(String(apiCall?.[1]?.body));
    }

    async function sentCreateArgs(fetchMock: ReturnType<typeof stubJmapFetch>) {
      const body = await sentJmapBody(fetchMock);
      return body.methodCalls.find((call: [string]) => call[0] === 'Email/set')[1].create.create1;
    }

    async function sentSubmissionArgs(fetchMock: ReturnType<typeof stubJmapFetch>) {
      const body = await sentJmapBody(fetchMock);
      return body.methodCalls.find((call: [string]) => call[0] === 'EmailSubmission/set')[1];
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

    it('uses the identity discovered through Identity/get', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch('work-identity');

      await sendEmail({
        to: 'bob@example.com',
        subject: 'Hi',
        body: 'Hello',
        format: 'plain',
      });

      const identityCall = fetchMock.mock.calls.find((call) => {
        if (call[0] !== '/api/') return false;
        const body = JSON.parse(String(call[1]?.body));
        return body.methodCalls.some((methodCall: [string]) => methodCall[0] === 'Identity/get');
      });
      expect(identityCall).toBeDefined();
      const submission = await sentSubmissionArgs(fetchMock);
      expect(submission.create.submission1.identityId).toBe('work-identity');
      expect(submission.create.submission1.identityId).not.toBe('primary');
    });

    it('prefers the non-deletable identity when several are returned', async () => {
      const { sendEmail } = await importApi();
      const fetchMock = stubJmapFetch('main-identity', [{ id: 'alias-identity', mayDelete: true }]);

      await sendEmail({
        to: 'bob@example.com',
        subject: 'Hi',
        body: 'Hello',
        format: 'plain',
      });

      const submission = await sentSubmissionArgs(fetchMock);
      expect(submission.create.submission1.identityId).toBe('main-identity');
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

  describe('isPasswordChangeSupported', () => {
    it('calls the capability endpoint once and caches the result in memory', async () => {
      const { isPasswordChangeSupported } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (_input: RequestInfo | URL) => createMockResponse({ supported: true }));
      vi.stubGlobal('fetch', fetchMock);

      const [first, second] = await Promise.all([
        isPasswordChangeSupported(),
        isPasswordChangeSupported(),
      ]);

      expect(first).toBe(true);
      expect(second).toBe(true);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0][0]).toBe('/api/user/password-change-supported');
      // The answer lives only in the module cache, never in browser storage.
      expect(window.localStorage.length).toBe(1);
    });

    it('caches a failed check instead of retrying it', async () => {
      const { isPasswordChangeSupported } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (_input: RequestInfo | URL) => createMockResponse(
        { error: 'check failed' },
        { status: 500, ok: false },
      ));
      vi.stubGlobal('fetch', fetchMock);

      await expect(isPasswordChangeSupported()).rejects.toThrow('check failed');
      await expect(isPasswordChangeSupported()).rejects.toThrow('check failed');

      expect(fetchMock).toHaveBeenCalledTimes(1);
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

    it('refreshes and retries a JMAP blob download with the new access token', async () => {
      const { getRawEmail } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'old-token');
      window.localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1');
      let downloadAttempt = 0;
      const downloadURL = '/download/acct_alice/blob-1/message.eml?type=message%2Frfc822';
      const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = String(input);
        if (url === '/api/auth/refresh') return createMockResponse(refreshedTokens);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            ...jmapSession,
            apiUrl: '/api/jmap/',
            downloadUrl: '/download/{accountId}/{blobId}/{name}?type={type}',
            primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct_alice' },
          });
        }
        if (url === '/api/jmap/') {
          return createMockResponse({
            methodResponses: [[
              'Email/get',
              { accountId: 'acct_alice', list: [{ id: 'e1', blobId: 'blob-1' }] },
              'raw1',
            ]],
            sessionState: 's1',
          });
        }
        if (url === downloadURL) {
          downloadAttempt += 1;
          return downloadAttempt === 1
            ? createMockResponse({ detail: 'unauthorized' }, { status: 401, ok: false })
            : createMockResponse('From: alice@example.com\r\n\r\nBody');
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await getRawEmail('e1');

      expect(result).toContain('Body');
      expect(fetchMock.mock.calls.filter((call) => String(call[0]) === '/api/auth/refresh')).toHaveLength(1);
      expect(authHeadersFor(fetchMock, downloadURL)).toEqual([
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

  describe('vacation response', () => {
    const VACATION_CAPABILITY = 'urn:ietf:params:jmap:vacationresponse';

    const stubSession = (fetchMock: ReturnType<typeof vi.fn>) => {
      fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { [VACATION_CAPABILITY]: 'test@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [
              ['VacationResponse/get', {
                accountId: 'test@example.com',
                state: 'v1',
                list: [{ id: 'singleton', isEnabled: true, subject: 'Away' }],
                notFound: [],
              }, 'getVacation'],
            ],
            sessionState: '1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
    };

    it('loads the singleton using the vacationresponse capability', async () => {
      const { getVacationResponse } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn();
      stubSession(fetchMock);
      vi.stubGlobal('fetch', fetchMock);

      const result = await getVacationResponse();

      expect(result).toEqual({ id: 'singleton', isEnabled: true, subject: 'Away' });
      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/');
      const body = JSON.parse((apiCall as unknown as [string, RequestInit])[1].body as string);
      expect(body.using).toContain(VACATION_CAPABILITY);
      expect(body.methodCalls[0][0]).toBe('VacationResponse/get');
      expect(body.methodCalls[0][1].accountId).toBe('test@example.com');
      expect(body.methodCalls[0][1].ids).toContain('singleton');
    });

    it('sends a VacationResponse/set patch and returns the updated singleton', async () => {
      const { setVacationResponse } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { [VACATION_CAPABILITY]: 'test@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [
              ['VacationResponse/set', {
                accountId: 'test@example.com',
                oldState: 'v1',
                newState: 'v2',
                created: {},
                updated: { singleton: { id: 'singleton', isEnabled: true, subject: 'Away' } },
                destroyed: [],
                notCreated: {},
                notUpdated: {},
                notDestroyed: {},
              }, 'setVacation'],
            ],
            sessionState: '2',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      const result = await setVacationResponse({ isEnabled: true, subject: 'Away', textBody: null });

      expect(result.subject).toBe('Away');
      const apiCall = fetchMock.mock.calls.find((call) => call[0] === '/api/');
      const body = JSON.parse((apiCall as unknown as [string, RequestInit])[1].body as string);
      expect(body.using).toContain(VACATION_CAPABILITY);
      expect(body.methodCalls[0][0]).toBe('VacationResponse/set');
      expect(body.methodCalls[0][1].update.singleton).toEqual({
        isEnabled: true,
        subject: 'Away',
        textBody: null,
      });
    });

    it('throws the server description when notUpdated reports an error', async () => {
      const { setVacationResponse } = await importApi();
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/.well-known/jmap') {
          return createMockResponse({
            username: 'test@example.com',
            apiUrl: '/api/',
            primaryAccounts: { [VACATION_CAPABILITY]: 'test@example.com' },
          });
        }
        if (url === '/api/') {
          return createMockResponse({
            methodResponses: [
              ['VacationResponse/set', {
                accountId: 'test@example.com',
                oldState: 'v1',
                newState: 'v1',
                created: {},
                updated: {},
                destroyed: [],
                notCreated: {},
                notUpdated: { singleton: { type: 'invalidProperties', description: 'bad date' } },
                notDestroyed: {},
              }, 'setVacation'],
            ],
            sessionState: '1',
          });
        }
        throw new Error(`Unexpected fetch: ${url}`);
      });
      vi.stubGlobal('fetch', fetchMock);

      await expect(setVacationResponse({ isEnabled: true })).rejects.toThrow('bad date');
    });
  });
});