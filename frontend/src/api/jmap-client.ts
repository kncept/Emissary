import type {
  JmapRequest,
  JmapResponse,
  Session,
  Invocation,
} from './generated/schemas/jmap';

const SESSION_URL = '/.well-known/jmap';
export const MASQUERADE_AS_KEY = 'masquerade_as';

// JMAP method call types
interface JmapMethodCall {
  method: string;
  args: Record<string, unknown>;
  id: string;
}

interface JmapMethodResponse {
  method: string;
  args: Record<string, unknown>;
  id: string;
}

let sessionPromise: Promise<Session> | null = null;

/**
 * Creates a JMAP request with multiple method calls
 */
function createJmapRequest(
  using: string[],
  methodCalls: JmapMethodCall[]
): JmapRequest {
  return {
    using,
    methodCalls: methodCalls.map((call) => [call.method, call.args, call.id] as Invocation),
  };
}

/**
 * Parses JMAP response and returns method responses by ID
 */
function parseJmapResponse(response: JmapResponse): Map<string, JmapMethodResponse> {
  const result = new Map<string, JmapMethodResponse>();
  for (const [method, args, id] of response.methodResponses) {
    result.set(id, { method, args: args as Record<string, unknown>, id });
  }
  return result;
}

function getToken(): string | null {
  return window.localStorage.getItem('emissary_token');
}

async function authenticatedFetch(url: string, options: RequestInit = {}): Promise<Response> {
  const token = getToken();
  const headers = new Headers(options.headers);
  if (!headers.has('Content-Type') && options.body != null) {
    headers.set('Content-Type', 'application/json');
  }
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    window.localStorage.removeItem('emissary_token');
    window.localStorage.removeItem('emissary_refresh_token');
    sessionPromise = null;
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  return response;
}

async function fetchJmapSession(): Promise<Session> {
  const response = await authenticatedFetch(SESSION_URL);
  if (!response.ok) {
    throw new Error('Failed to get JMAP session');
  }
  return response.json();
}

async function getJmapSession(): Promise<Session> {
  if (!sessionPromise) {
    sessionPromise = fetchJmapSession().catch((error) => {
      sessionPromise = null;
      throw error;
    });
  }
  return sessionPromise;
}

async function getAccountId(session: Session): Promise<string> {
  const mailAccount = session.primaryAccounts?.['urn:ietf:params:jmap:mail'];
  if (mailAccount) return mailAccount;
  const accountIds = session.accounts ? Object.keys(session.accounts) : [];
  if (accountIds.length > 0) return accountIds[0];
  return session.username;
}

async function jmapRequest(
  using: string[],
  methodCalls: JmapMethodCall[]
): Promise<Map<string, JmapMethodResponse>> {
  const session = await getJmapSession();
  
  // Check for masquerade target - when admin is masquerading as another user,
  // use the masquerade target as the accountId
  const masqueradeAs = window.localStorage.getItem(MASQUERADE_AS_KEY);
  const accountId = masqueradeAs || await getAccountId(session);
  
  const request = createJmapRequest(using, methodCalls);
  for (const call of request.methodCalls) {
    if (call[1] && typeof call[1] === 'object' && call[1] !== null) {
      const args = call[1] as Record<string, unknown>;
      if (args.accountId == null) args.accountId = accountId;
    }
  }

  const response = await authenticatedFetch(session.apiUrl, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  });

  if (!response.ok) {
    throw new Error('JMAP request failed');
  }
  const data = (await response.json()) as JmapResponse;
  return parseJmapResponse(data);
}

export interface Email {
  id: string;
  from: string;
  to: string;
  subject: string;
  body: string;
  timestamp: string;
  read: boolean;
  flagged: boolean;
}

function convertJmapEmail(jmapEmail: Record<string, unknown>): Email {
  const fromArray = jmapEmail.from as Array<{ name?: string; email: string }> | undefined;
  const toArray = jmapEmail.to as Array<{ name?: string; email: string }> | undefined;
  const keywords = jmapEmail.keywords as Record<string, boolean> | undefined;

  return {
    id: jmapEmail.id as string,
    from: fromArray?.[0]?.email || '',
    to: toArray?.[0]?.email || '',
    subject: (jmapEmail.subject as string) || '(No subject)',
    body: (jmapEmail.preview as string) || '',
    timestamp: (jmapEmail.receivedAt as string) || new Date().toISOString(),
    read: keywords?.$seen === true,
    flagged: keywords?.$flagged === true,
  };
}

export async function getEmails(_userFilter?: string): Promise<Email[]> {
  const methodId = 'query1';
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/query',
      args: {
        filter: { inMailbox: 'inbox' },
        sort: [{ property: 'receivedAt', isAscending: false }],
        collapseThreads: false,
      },
      id: methodId,
    },
    {
      method: 'Email/get',
      args: {
        '#ids': { resultOf: methodId, name: 'Email/query', path: '/ids/*' },
        properties: [
          'id',
          'from',
          'to',
          'subject',
          'preview',
          'receivedAt',
          'keywords',
          'mailboxIds',
        ],
      },
      id: 'get1',
    },
  ]);

  const getResponse = responses.get('get1');
  if (!getResponse || getResponse.method === 'error') {
    throw new Error('Failed to fetch emails');
  }

  const list = getResponse.args.list as Array<Record<string, unknown>> | undefined;
  if (!list) {
    return [];
  }

  return list.map(convertJmapEmail);
}

export async function getEmail(id: string): Promise<Email> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/get',
      args: {
        ids: [id],
        properties: [
          'id',
          'from',
          'to',
          'subject',
          'preview',
          'receivedAt',
          'keywords',
          'mailboxIds',
          'textBody',
          'bodyValues',
        ],
        fetchTextBodyValues: true,
      },
      id: 'get1',
    },
  ]);

  const getResponse = responses.get('get1');
  if (!getResponse || getResponse.method === 'error') {
    throw new Error('Failed to fetch email');
  }

  const list = getResponse.args.list as Array<Record<string, unknown>> | undefined;
  if (!list || list.length === 0) {
    throw new Error('Email not found');
  }

  const email = convertJmapEmail(list[0]);
  const textBody = list[0].textBody as Array<Record<string, unknown>> | undefined;
  const bodyValues = list[0].bodyValues as Record<string, { value: string }> | undefined;

  if (textBody && textBody.length > 0 && bodyValues) {
    const partId = textBody[0].partId as string | undefined;
    const blobId = textBody[0].blobId as string | undefined;
    if (partId && bodyValues[partId]) {
      email.body = bodyValues[partId].value;
    } else if (blobId && bodyValues[blobId]) {
      email.body = bodyValues[blobId].value;
    }
  }

  return email;
}

export async function markRead(id: string, read: boolean): Promise<void> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/set',
      args: {
        update: {
          [id]: {
            'keywords/$seen': read,
          },
        },
      },
      id: 'set1',
    },
  ]);

  const setResponse = responses.get('set1');
  if (!setResponse || setResponse.method === 'error') {
    throw new Error('Failed to update read status');
  }

  const notUpdated = setResponse.args.notUpdated as Record<string, { type: string }> | undefined;
  if (notUpdated && notUpdated[id]) {
    throw new Error(`Failed to update read status: ${notUpdated[id].type}`);
  }
}

export async function markFlagged(id: string, flagged: boolean): Promise<void> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/set',
      args: {
        update: {
          [id]: {
            'keywords/$flagged': flagged,
          },
        },
      },
      id: 'set1',
    },
  ]);

  const setResponse = responses.get('set1');
  if (!setResponse || setResponse.method === 'error') {
    throw new Error('Failed to update flagged status');
  }

  const notUpdated = setResponse.args.notUpdated as Record<string, { type: string }> | undefined;
  if (notUpdated && notUpdated[id]) {
    throw new Error(`Failed to update flagged status: ${notUpdated[id].type}`);
  }
}

export async function deleteEmail(id: string): Promise<void> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/set',
      args: {
        destroy: [id],
      },
      id: 'set1',
    },
  ]);

  const setResponse = responses.get('set1');
  if (!setResponse || setResponse.method === 'error') {
    throw new Error('Failed to delete email');
  }

  const notDestroyed = setResponse.args.notDestroyed as Record<string, { type: string }> | undefined;
  if (notDestroyed && notDestroyed[id]) {
    throw new Error(`Failed to delete email: ${notDestroyed[id].type}`);
  }
}

export async function sendEmail(data: {
  from?: string;
  to: string;
  subject: string;
  body: string;
  replyToId?: string;
}): Promise<{ success: boolean; message?: string }> {
  const createId = 'create1';
  const submissionId = 'submission1';
  const bodySize = new TextEncoder().encode(data.body).length;

  const emailCreateArgs: Record<string, unknown> = {
    create: {
      [createId]: {
        mailboxIds: { drafts: true },
        keywords: { $draft: true },
        from: [{ email: data.from || '', name: '' }],
        to: [{ email: data.to, name: '' }],
        subject: data.subject,
        textBody: [{ partId: 'text1', type: 'text/plain', size: bodySize }],
        bodyValues: {
          text1: { value: data.body, isEncodingProblem: false, isTruncated: false },
        },
      },
    },
  };

  if (data.replyToId) {
    const originalEmail = await getEmail(data.replyToId);
    if (originalEmail) {
      const createArgs = (emailCreateArgs.create as Record<string, Record<string, unknown>>)[createId];
      createArgs.inReplyTo = [originalEmail.id];
      createArgs.references = [originalEmail.id];
    }
  }

  const responses = await jmapRequest(['urn:ietf:params:jmap:mail', 'urn:ietf:params:jmap:submission'], [
    {
      method: 'Email/set',
      args: emailCreateArgs,
      id: 'emailCreate',
    },
    {
      method: 'EmailSubmission/set',
      args: {
        create: {
          [submissionId]: {
            identityId: 'primary',
            emailId: `#${createId}`,
            envelope: data.from ? {
              mailFrom: { email: data.from },
              rcptTo: [{ email: data.to }],
            } : undefined,
          },
        },
        onSuccessUpdateEmail: {
          [`#${submissionId}`]: {
            'keywords/$draft': null,
          },
        },
      },
      id: 'submissionCreate',
    },
  ]);

  const emailCreateResponse = responses.get('emailCreate');
  if (!emailCreateResponse || emailCreateResponse.method === 'error') {
    throw new Error('Failed to create email for sending');
  }

  const notCreated = emailCreateResponse.args.notCreated as Record<string, { type: string }> | undefined;
  if (notCreated && notCreated[createId]) {
    throw new Error(`Failed to create email: ${notCreated[createId].type}`);
  }

  const submissionResponse = responses.get('submissionCreate');
  if (!submissionResponse || submissionResponse.method === 'error') {
    throw new Error('Failed to send email (submission error)');
  }

  const submissionNotCreated = submissionResponse.args.notCreated as Record<string, { type: string }> | undefined;
  if (submissionNotCreated && submissionNotCreated[submissionId]) {
    throw new Error(`Failed to send email: ${submissionNotCreated[submissionId].type}`);
  }

  return { success: true, message: 'Email sent' };
}