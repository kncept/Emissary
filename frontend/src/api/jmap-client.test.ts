import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const TOKEN_KEY = 'emissary_token';
const REFRESH_KEY = 'emissary_refresh_token';
const MASQUERADE_KEY = 'masquerade_as';

const MAIL = 'urn:ietf:params:jmap:mail';
const SUBMISSION = 'urn:ietf:params:jmap:submission';
const VACATION = 'urn:ietf:params:jmap:vacationresponse';
const ACCOUNT = 'acct_alice';
const API_URL = 'https://jmap.example.test/api/jmap/';
const DOWNLOAD_TEMPLATE =
  'https://jmap.example.test/download/{accountId}/{blobId}/{name}?type={type}';
const SESSION_URL = '/.well-known/jmap';
const REFRESH_URL = '/api/auth/refresh';

type MethodCall = [string, Record<string, unknown>, string];
type RequestBody = { using: string[]; methodCalls: MethodCall[] };

interface CapturedCall {
  url: string;
  method?: string;
  headers: Headers;
  body: RequestBody | null;
}

const session = {
  capabilities: { [MAIL]: {}, [SUBMISSION]: {}, [VACATION]: {} },
  accounts: {
    [ACCOUNT]: { name: 'alice@example.com', isPersonal: true, isReadOnly: false },
  },
  primaryAccounts: {
    [MAIL]: ACCOUNT,
    [SUBMISSION]: ACCOUNT,
    [VACATION]: ACCOUNT,
  },
  username: 'alice@example.com',
  apiUrl: API_URL,
  downloadUrl: DOWNLOAD_TEMPLATE,
  uploadUrl: 'https://jmap.example.test/upload/{accountId}',
  eventSourceUrl: 'https://stream.example.test/eventsource/stream?closeafter=no',
  state: 'session-1',
};

function response(data: unknown, status = 200) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => data,
    text: async () => (typeof data === 'string' ? data : JSON.stringify(data)),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as Response;
}

function methodResponses(responses: [string, Record<string, unknown>, string][]) {
  return { methodResponses: responses, sessionState: 'session-2' };
}

// The default answer services mailbox discovery, then returns an empty inbox.
function defaultAnswer(body: RequestBody | null): Record<string, unknown> {
  const methods = body?.methodCalls.map((call) => call[0]) ?? [];
  if (methods.includes('Mailbox/query')) {
    return methodResponses([
      ['Mailbox/query', { accountId: ACCOUNT, state: 'm1', ids: ['mb-inbox', 'mb-drafts'] }, 'mailboxQuery'],
      ['Mailbox/get', {
        accountId: ACCOUNT,
        state: 'm1',
        list: [
          { id: 'mb-inbox', name: 'Inbox', role: 'inbox' },
          { id: 'mb-drafts', name: 'Drafts', role: 'drafts' },
        ],
        notFound: [],
      }, 'mailboxGet'],
    ]);
  }
  if (methods.includes('Identity/get')) {
    return methodResponses([
      ['Identity/get', {
        accountId: ACCOUNT,
        state: 'i1',
        list: [{ id: 'identity-primary', name: 'Alice', mayDelete: false }],
        notFound: [],
      }, 'identityGet'],
    ]);
  }
  if (methods.includes('EmailSubmission/set')) {
    return methodResponses([
      ['Email/set', { accountId: ACCOUNT, created: { create1: { id: 'email-new' } }, notCreated: {} }, 'emailCreate'],
      ['EmailSubmission/set', { accountId: ACCOUNT, created: { submission1: { id: 'sub-new' } }, notCreated: {} }, 'submissionCreate'],
    ]);
  }
  return methodResponses([['Email/get', { accountId: ACCOUNT, state: 'e1', list: [], notFound: [] }, 'get1']]);
}

let calls: CapturedCall[];
let answer: (body: RequestBody | null) => Record<string, unknown>;
let rawMIME: string;
let unauthorizedOnce: boolean;

function installFetch() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const headers = new Headers(init?.headers);
    let body: RequestBody | null = null;
    if (typeof init?.body === 'string') {
      try {
        body = JSON.parse(init.body) as RequestBody;
      } catch {
        body = null;
      }
    }
    calls.push({ url, method: init?.method, headers, body });

    if (url === SESSION_URL) return response(session);
    if (url === REFRESH_URL) {
      return response({
        accessToken: 'new-token',
        refreshToken: 'new-refresh',
        tokenType: 'Bearer',
        expiresAt: '2099-01-01T00:00:00Z',
      });
    }
    if (url === API_URL) {
      if (unauthorizedOnce) {
        unauthorizedOnce = false;
        return response({ detail: 'token expired' }, 401);
      }
      return response(answer(body));
    }
    if (url.startsWith('https://jmap.example.test/download/')) {
      return response(rawMIME);
    }
    throw new Error(`Unexpected fetch: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function importClient() {
  vi.resetModules();
  return await import('./jmap-client.ts');
}

const apiCalls = () => calls.filter((call) => call.url === API_URL);
const callNamed = (name: string, body: RequestBody): MethodCall =>
  body.methodCalls.find((call) => call[0] === name)!;

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem(TOKEN_KEY, 'old-token');
  window.localStorage.setItem(REFRESH_KEY, 'old-refresh');
  calls = [];
  answer = defaultAnswer;
  rawMIME = 'From: alice@example.com\r\nSubject: raw\r\n\r\nMIME body';
  unauthorizedOnce = false;
  installFetch();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('JMAP client request envelope', () => {
  it('builds [name, args, id] calls, opts into mail, and injects the opaque account id', async () => {
    const { getEmails } = await importClient();
    await getEmails();

    const posts = apiCalls();
    expect(posts.length).toBeGreaterThanOrEqual(2);
    const inboxCall = posts.find((post) =>
      post.body?.methodCalls.some((call) => call[0] === 'Email/query'));
    expect(inboxCall).toBeDefined();
    const body = inboxCall!.body!;
    expect(body.using).toEqual([MAIL]);
    expect(body.methodCalls.map((call) => call[0])).toEqual(['Email/query', 'Email/get']);
    for (const [, args, id] of body.methodCalls) {
      expect(args.accountId).toBe(ACCOUNT);
      expect(id).toMatch(/^[a-z0-9]+$/i);
    }
  });

  it('passes an Email/query result reference to Email/get via #ids', async () => {
    const { getEmails } = await importClient();
    await getEmails();

    const body = apiCalls().find((post) =>
      post.body?.methodCalls.some((call) => call[0] === 'Email/query'))!.body!;
    const getArgs = callNamed('Email/get', body)[1];
    expect(getArgs['#ids']).toEqual({
      resultOf: 'query1',
      name: 'Email/query',
      path: '/ids/*',
    });
  });

  it('discovers the inbox mailbox instead of using a literal id', async () => {
    const { getEmails } = await importClient();
    await getEmails();

    const discovery = apiCalls()[0].body!;
    expect(discovery.using).toEqual([MAIL]);
    expect(discovery.methodCalls.map((call) => call[0])).toEqual(['Mailbox/query', 'Mailbox/get']);
    expect(callNamed('Mailbox/query', discovery)[1].filter).toEqual({ role: 'inbox' });
    expect(callNamed('Mailbox/get', discovery)[1]['#ids']).toEqual({
      resultOf: 'mailboxQuery',
      name: 'Mailbox/query',
      path: '/ids/*',
    });

    const inbox = apiCalls()[1].body!;
    expect(callNamed('Email/query', inbox)[1].filter).toEqual({ inMailbox: 'mb-inbox' });
  });

  it('discovers and queries a non-inbox role mailbox', async () => {
    answer = (body) => {
      const methods = body?.methodCalls.map((call) => call[0]) ?? [];
      if (methods.includes('Mailbox/query')) {
        return methodResponses([
          ['Mailbox/query', { accountId: ACCOUNT, state: 'm1', ids: ['mb-sent'] }, 'mailboxQuery'],
          ['Mailbox/get', {
            accountId: ACCOUNT,
            state: 'm1',
            list: [{ id: 'mb-sent', name: 'Sent', role: 'sent' }],
            notFound: [],
          }, 'mailboxGet'],
        ]);
      }
      return methodResponses([
        ['Email/query', { accountId: ACCOUNT, queryState: '1', ids: ['s1'], total: 1 }, 'query1'],
        ['Email/get', {
          accountId: ACCOUNT,
          state: '1',
          list: [{
            id: 's1',
            from: [{ email: ACCOUNT }],
            to: [{ email: 'bob@example.com' }],
            subject: 'Sent item',
            preview: 'Hi Bob',
            receivedAt: '2026-11-20T00:00:00Z',
            keywords: {},
          }],
          notFound: [],
        }, 'get1'],
      ]);
    };

    const { getEmails } = await importClient();
    const emails = await getEmails('sent');
    expect(emails.map((email) => email.subject)).toEqual(['Sent item']);

    const discovery = apiCalls()[0].body!;
    expect(callNamed('Mailbox/query', discovery)[1].filter).toEqual({ role: 'sent' });
    const query = apiCalls()[1].body!;
    expect(callNamed('Email/query', query)[1].filter).toEqual({ inMailbox: 'mb-sent' });
  });
});

describe('JMAP client send flow', () => {
  it('uses creation ids and result refs between Email/set and EmailSubmission/set', async () => {
    const { sendEmail } = await importClient();
    await sendEmail({ to: 'bob@example.com', subject: 'Hi', body: 'Hello', format: 'plain' });

    const sendCall = apiCalls().find((post) =>
      post.body?.methodCalls.some((call) => call[0] === 'EmailSubmission/set'));
    expect(sendCall).toBeDefined();
    const body = sendCall!.body!;
    expect(body.using).toEqual([MAIL, SUBMISSION]);

    const emailArgs = callNamed('Email/set', body)[1];
    const create = emailArgs.create as Record<string, Record<string, unknown>>;
    expect(create.create1.mailboxIds).toEqual({ 'mb-drafts': true });
    // `size` is server-set on EmailBodyPart; sending it gets invalidProperties.
    expect(create.create1.textBody).toEqual([{ partId: 'text1', type: 'text/plain' }]);

    const submissionArgs = callNamed('EmailSubmission/set', body)[1];
    const submissions = submissionArgs.create as Record<string, Record<string, unknown>>;
    expect(submissions.submission1.emailId).toBe('#create1');
    expect(submissions.submission1.identityId).toBe('identity-primary');
    expect(submissions.submission1.envelope).toEqual({
      mailFrom: { email: '' },
      rcptTo: [{ email: 'bob@example.com' }],
    });
    expect((submissionArgs.onSuccessUpdateEmail as Record<string, unknown>)['#submission1'])
      .toEqual({ 'keywords/$draft': null });
  });

  it('keeps the submission response when an implicit Email/set shares its id', async () => {
    answer = (body) => {
      if (body?.methodCalls.some((call) => call[0] === 'EmailSubmission/set')) {
        return methodResponses([
          ['Email/set', { accountId: ACCOUNT, created: { create1: { id: 'email-new' } }, notCreated: {} }, 'emailCreate'],
          ['EmailSubmission/set', { accountId: ACCOUNT, created: { submission1: { id: 'sub-new' } }, notCreated: {} }, 'submissionCreate'],
          ['Email/set', { accountId: ACCOUNT, updated: { 'email-new': {} }, notUpdated: {} }, 'submissionCreate'],
        ]);
      }
      return defaultAnswer(body);
    };

    const { sendEmail } = await importClient();
    await expect(sendEmail({
      to: 'bob@example.com', subject: 'Hi', body: 'Hello', format: 'plain',
    })).resolves.toEqual({ success: true, message: 'Email sent' });
  });

  it('does not let an implicit Email/set mask a submission error', async () => {
    answer = (body) => {
      if (body?.methodCalls.some((call) => call[0] === 'EmailSubmission/set')) {
        return methodResponses([
          ['Email/set', { accountId: ACCOUNT, created: { create1: { id: 'email-new' } }, notCreated: {} }, 'emailCreate'],
          ['EmailSubmission/set', {
            accountId: ACCOUNT,
            created: {},
            notCreated: { submission1: { type: 'forbidden', description: 'Not allowed' } },
          }, 'submissionCreate'],
          ['Email/set', { accountId: ACCOUNT, updated: {}, notUpdated: {} }, 'submissionCreate'],
        ]);
      }
      return defaultAnswer(body);
    };

    const { sendEmail } = await importClient();
    await expect(sendEmail({
      to: 'bob@example.com', subject: 'Hi', body: 'Hello', format: 'plain',
    })).rejects.toThrow('Failed to send email: forbidden');
  });

  it('opts Identity/get into the submission capability', async () => {
    const { sendEmail } = await importClient();
    await sendEmail({ to: 'bob@example.com', subject: 'Hi', body: 'Hello', format: 'plain' });

    const identityCall = apiCalls().find((post) =>
      post.body?.methodCalls.some((call) => call[0] === 'Identity/get'));
    expect(identityCall?.body?.using).toEqual([SUBMISSION]);
  });

  it('uses the vacationresponse capability and the singleton id', async () => {
    answer = () => methodResponses([
      ['VacationResponse/get', {
        accountId: ACCOUNT,
        state: 'v1',
        list: [{ id: 'singleton', isEnabled: false }],
        notFound: [],
      }, 'getVacation'],
    ]);
    const { getVacationResponse } = await importClient();

    const vacation = await getVacationResponse();
    expect(vacation.id).toBe('singleton');
    const body = apiCalls()[0].body!;
    expect(body.using).toEqual([VACATION]);
    expect(callNamed('VacationResponse/get', body)[1].ids).toEqual(['singleton']);
  });
});

describe('JMAP client account injection', () => {
  it('converts a stored masquerade email into the opaque JMAP account id', async () => {
    window.localStorage.setItem(MASQUERADE_KEY, 'Victim@Example.com');
    const digest = await crypto.subtle.digest(
      'SHA-256',
      new TextEncoder().encode('victim@example.com'),
    );
    const encoded = btoa(String.fromCharCode(...new Uint8Array(digest)))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
    const expected = `acct_${encoded}`;

    const { getEmails } = await importClient();
    await getEmails();

    expect(apiCalls().length).toBeGreaterThan(0);
    for (const post of apiCalls()) {
      for (const [, args] of post.body!.methodCalls) {
        expect(args.accountId).toBe(expected);
      }
    }
    expect(expected).not.toBe(ACCOUNT);
  });
});

describe('JMAP client errors and auth', () => {
  it('surfaces SetErrors from notCreated / notUpdated / notDestroyed', async () => {
    const { deleteEmail, markRead, sendEmail } = await importClient();

    answer = () => methodResponses([
      ['Email/set', { accountId: ACCOUNT, notDestroyed: { e1: { type: 'forbidden' } } }, 'set1'],
    ]);
    await expect(deleteEmail('e1')).rejects.toThrow('forbidden');

    answer = () => methodResponses([
      ['Email/set', { accountId: ACCOUNT, notUpdated: { e1: { type: 'notFound' } } }, 'set1'],
    ]);
    await expect(markRead('e1', true)).rejects.toThrow('notFound');

    answer = (body) => {
      const methods = body?.methodCalls.map((call) => call[0]) ?? [];
      if (methods.includes('Mailbox/query')) return defaultAnswer(body);
      if (methods.includes('Identity/get')) return defaultAnswer(body);
      return methodResponses([
        ['Email/set', { accountId: ACCOUNT, created: {}, notCreated: { create1: { type: 'invalidProperties' } } }, 'emailCreate'],
      ]);
    };
    await expect(sendEmail({
      to: 'bob@example.com', subject: 'Hi', body: 'Hello', format: 'plain',
    })).rejects.toThrow('invalidProperties');
  });

  it('refreshes once on a 401 and retries with the new bearer token', async () => {
    unauthorizedOnce = true;
    const { getEmails } = await importClient();
    await getEmails();

    const refreshCalls = calls.filter((call) => call.url === REFRESH_URL);
    expect(refreshCalls).toHaveLength(1);
    const apiAuth = apiCalls().map((call) => call.headers.get('Authorization'));
    expect(apiAuth).toContain('Bearer old-token');
    expect(apiAuth).toContain('Bearer new-token');
    expect(window.localStorage.getItem(TOKEN_KEY)).toBe('new-token');
    expect(window.localStorage.getItem(REFRESH_KEY)).toBe('new-refresh');
  });

  it('raises a method-level error response instead of returning a partial list', async () => {
    answer = () => methodResponses([
      ['error', { type: 'unknownMethod', description: 'unsupported' }, 'get1'],
    ]);
    const { getEmails } = await importClient();

    // Discovery still succeeds; the Email/get call for the inbox fails.
    answer = (body) => {
      const methods = body?.methodCalls.map((call) => call[0]) ?? [];
      if (methods.includes('Mailbox/query')) return defaultAnswer(body);
      return methodResponses([
        ['error', { type: 'unknownMethod', description: 'unsupported' }, 'get1'],
      ]);
    };
    await expect(getEmails()).rejects.toThrow('Failed to fetch emails');
  });
});

describe('raw MIME via blobId (RESOLVED-4)', () => {
  it('fetches Email/get blobId through the Session downloadUrl template', async () => {
    answer = () => methodResponses([
      ['Email/get', { accountId: ACCOUNT, state: 'e1', list: [{ id: 'e1', blobId: 'blob-42' }], notFound: [] }, 'raw1'],
    ]);
    const { getRawEmail } = await importClient();

    await expect(getRawEmail('e1')).resolves.toBe(rawMIME);

    const getCall = apiCalls()[0];
    expect(callNamed('Email/get', getCall.body!)[1].properties).toEqual(['id', 'blobId']);
    const download = calls.find((call) =>
      call.url.startsWith('https://jmap.example.test/download/'));
    expect(download?.url).toBe(
      'https://jmap.example.test/download/acct_alice/blob-42/message.eml?type=message%2Frfc822',
    );
    // The only fetch URLs are Session, JMAP API, optional refresh, and the
    // blob download above; no legacy REST MIME route is involved.
    for (const call of calls) {
      expect(
        [SESSION_URL, API_URL, REFRESH_URL].includes(call.url) ||
          call.url.startsWith('https://jmap.example.test/download/'),
      ).toBe(true);
    }
  });
});
