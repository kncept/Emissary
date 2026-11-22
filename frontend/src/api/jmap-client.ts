import type {
  JmapRequest,
  JmapResponse,
  Session,
  Invocation,
  VacationResponse,
  VacationResponseUpdate,
} from './generated/schemas/jmap';
import { htmlToText } from '../html.ts';
import type { SendEmailRequest } from '../types.ts';
import { authenticatedFetch } from './auth-fetch.ts';
import { recordEmailCheck } from './email-checks.ts';

const SESSION_URL = '/.well-known/jmap';
export const MASQUERADE_AS_KEY = 'masquerade_as';

const VACATION_CAPABILITY = 'urn:ietf:params:jmap:vacationresponse';
const VACATION_SINGLETON_ID = 'singleton';

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

// authenticatedFetch (shared with the REST client) refreshes the access token
// on a 401 and retries before giving up and clearing the session.

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

// Mirrors auth.AccountIDForEmail on the server: lowercase/trim the email,
// SHA-256 it, and encode it as an RFC 8620-safe id with an acct_ prefix.
async function accountIDForEmail(email: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(email.trim().toLowerCase())
  );
  const bytes = Array.from(new Uint8Array(digest));
  const base64 = btoa(bytes.map((byte) => String.fromCharCode(byte)).join(''))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
  return `acct_${base64}`;
}

// An administrator masquerading as another user targets that account for every
// method call, regardless of the Session's personal primary account. The UI
// stores the masquerade target's email, so convert it to the opaque JMAP id.
async function resolveAccountId(session: Session): Promise<string> {
  const masqueradeAs = window.localStorage.getItem(MASQUERADE_AS_KEY);
  if (masqueradeAs) return accountIDForEmail(masqueradeAs);
  return getAccountId(session);
}

// RFC 6570 level-1 substitution; all variables are simple string values.
function expandURITemplate(template: string, values: Record<string, string>): string {
  return template.replace(/\{([A-Za-z0-9_]+)\}/g, (_match, name: string) =>
    encodeURIComponent(values[name] ?? '')
  );
}

async function jmapRequest(
  using: string[],
  methodCalls: JmapMethodCall[]
): Promise<Map<string, JmapMethodResponse>> {
  const session = await getJmapSession();
  const accountId = await resolveAccountId(session);

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
  html?: string;
  timestamp: string;
  read: boolean;
  flagged: boolean;
  /** RFC 5322 Message-ID of the message (with angle brackets). */
  messageId?: string;
  /** Original References header values, oldest first. */
  references?: string[];
  /** Reply-To addresses; replies go here when present, otherwise to From. */
  replyTo?: string[];
}

function convertJmapEmail(jmapEmail: Record<string, unknown>): Email {
  const fromArray = jmapEmail.from as Array<{ name?: string; email: string }> | undefined;
  const toArray = jmapEmail.to as Array<{ name?: string; email: string }> | undefined;
  const replyToArray = jmapEmail.replyTo as Array<{ name?: string; email: string }> | undefined;
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
    messageId: (jmapEmail.messageId as string[] | undefined)?.[0] || undefined,
    references: (jmapEmail.references as string[] | undefined) || undefined,
    replyTo:
      replyToArray && replyToArray.length > 0
        ? replyToArray.map((address) => address.email)
        : undefined,
  };
}

/**
 * Parses a comma-separated recipient field ("a@example.com, b@example.com")
 * into a clean address list, dropping empties and trimming whitespace.
 */
export function parseAddressList(input: string): string[] {
  return input
    .split(',')
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
}

export async function getEmails(_userFilter?: string): Promise<Email[]> {
  // Any check of the inbox, wherever it comes from, restarts the update timer.
  recordEmailCheck();
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
          'replyTo',
          'messageId',
          'inReplyTo',
          'references',
          'subject',
          'preview',
          'receivedAt',
          'keywords',
          'mailboxIds',
          'textBody',
          'htmlBody',
          'bodyValues',
        ],
        fetchTextBodyValues: true,
        fetchHTMLBodyValues: true,
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

  const htmlBody = list[0].htmlBody as Array<Record<string, unknown>> | undefined;
  if (htmlBody && htmlBody.length > 0 && bodyValues) {
    const partId = htmlBody[0].partId as string | undefined;
    const blobId = htmlBody[0].blobId as string | undefined;
    if (partId && bodyValues[partId]) {
      email.html = bodyValues[partId].value;
    } else if (blobId && bodyValues[blobId]) {
      email.html = bodyValues[blobId].value;
    }
  }

  return email;
}

/**
 * Fetches the original RFC 5322 bytes the JMAP way: Email/get exposes the
 * message as a blobId, and the Session's downloadUrl serves that blob.
 */
export async function getRawEmail(id: string): Promise<string> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/get',
      args: { ids: [id], properties: ['id', 'blobId'] },
      id: 'raw1',
    },
  ]);

  const getResponse = responses.get('raw1');
  if (!getResponse || getResponse.method === 'error') {
    throw new Error('Failed to load raw email MIME');
  }
  const list = getResponse.args.list as Array<Record<string, unknown>> | undefined;
  const found = list?.find((item) => item.id === id);
  const blobId = found?.blobId as string | undefined;
  if (!blobId) {
    throw new Error('email not found');
  }

  const session = await getJmapSession();
  const accountId =
    (getResponse.args.accountId as string | undefined) || await resolveAccountId(session);
  const url = expandURITemplate(session.downloadUrl, {
    accountId,
    blobId,
    name: 'message.eml',
    type: 'message/rfc822',
  });

  const response = await authenticatedFetch(url);
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    const message = (data as { detail?: string; error?: string }).detail
      || (data as { error?: string }).error;
    throw new Error(message || 'Failed to load raw email MIME');
  }
  return response.text();
}

export async function markRead(id: string, read: boolean): Promise<void> {
  const responses = await jmapRequest(['urn:ietf:params:jmap:mail'], [
    {
      method: 'Email/set',
      args: {
        update: {
          [id]: {
            // JMAP patch semantics: null removes a keyword; false is invalid.
            'keywords/$seen': read ? true : null,
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
            // JMAP patch semantics: null removes a keyword; false is invalid.
            'keywords/$flagged': flagged ? true : null,
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

export async function sendEmail(data: SendEmailRequest): Promise<{ success: boolean; message?: string }> {
  const createId = 'create1';
  const submissionId = 'submission1';
  const isHtml = data.format === 'html';
  // Plain messages are sent as text/plain only. HTML messages include a plain
  // alternative so clients without HTML support still have a readable body.
  const plainText = isHtml ? htmlToText(data.body) : data.body;
  const textSize = new TextEncoder().encode(plainText).length;

  const to = parseAddressList(data.to);
  const cc = parseAddressList(data.cc || '');
  const bcc = parseAddressList(data.bcc || '');

  // The server locks the From address to the authenticated account (or the
  // masquerade target), so no From is supplied here.
  const createArgs: Record<string, unknown> = {
    mailboxIds: { drafts: true },
    keywords: { $draft: true },
    to: to.map((email) => ({ email, name: '' })),
    subject: data.subject,
    textBody: [{ partId: 'text1', type: 'text/plain', size: textSize }],
    bodyValues: {
      text1: { value: plainText, isEncodingProblem: false, isTruncated: false },
    },
  };

  if (cc.length > 0) {
    createArgs.cc = cc.map((email) => ({ email, name: '' }));
  }
  if (bcc.length > 0) {
    createArgs.bcc = bcc.map((email) => ({ email, name: '' }));
  }
  if (data.inReplyTo && data.inReplyTo.length > 0) {
    createArgs.inReplyTo = data.inReplyTo;
  }
  if (data.references && data.references.length > 0) {
    createArgs.references = data.references;
  }

  if (isHtml) {
    createArgs.htmlBody = [{
      partId: 'html1',
      type: 'text/html',
      size: new TextEncoder().encode(data.body).length,
    }];
    (createArgs.bodyValues as Record<string, unknown>).html1 = {
      value: data.body,
      isEncodingProblem: false,
      isTruncated: false,
    };
  }

  const emailCreateArgs: Record<string, unknown> = {
    create: { [createId]: createArgs },
  };

  // The envelope covers every recipient; Bcc appears only here, never in the
  // message headers. mailFrom is never sent - the server owns the sender.
  const envelopeRecipients = [...to, ...cc, ...bcc];
  const submission: Record<string, unknown> = {
    identityId: 'primary',
    emailId: `#${createId}`,
  };
  if (envelopeRecipients.length > 0) {
    submission.envelope = {
      rcptTo: envelopeRecipients.map((email) => ({ email })),
    };
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
        create: { [submissionId]: submission },
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

/**
 * Loads the single VacationResponse object for the current account.
 * accountId (including a masquerade target) is injected by jmapRequest.
 */
export async function getVacationResponse(): Promise<VacationResponse> {
  const responses = await jmapRequest([VACATION_CAPABILITY], [
    {
      method: 'VacationResponse/get',
      args: { ids: [VACATION_SINGLETON_ID] },
      id: 'getVacation',
    },
  ]);

  const response = responses.get('getVacation');
  if (!response || response.method === 'error') {
    throw new Error('Failed to load vacation response');
  }

  const notFound = response.args.notFound as string[] | undefined;
  if (notFound?.includes(VACATION_SINGLETON_ID)) {
    throw new Error('Vacation response not found');
  }
  const list = response.args.list as VacationResponse[] | undefined;
  if (!list || list.length === 0) {
    throw new Error('Vacation response not found');
  }
  return list[0];
}

/**
 * Applies a JMAP patch to the VacationResponse singleton. The server reports a
 * per-id error in notUpdated rather than failing the whole method call.
 */
export async function setVacationResponse(
  update: VacationResponseUpdate,
): Promise<VacationResponse> {
  const responses = await jmapRequest([VACATION_CAPABILITY], [
    {
      method: 'VacationResponse/set',
      args: { update: { [VACATION_SINGLETON_ID]: update } },
      id: 'setVacation',
    },
  ]);

  const response = responses.get('setVacation');
  if (!response || response.method === 'error') {
    throw new Error('Failed to save vacation response');
  }

  const notUpdated = response.args.notUpdated as
    | Record<string, { type: string; description?: string }>
    | undefined;
  const failure = notUpdated?.[VACATION_SINGLETON_ID];
  if (failure) {
    throw new Error(failure.description || `Failed to save vacation response: ${failure.type}`);
  }

  const updated = response.args.updated as
    | Record<string, VacationResponse | null>
    | undefined;
  const value = updated?.[VACATION_SINGLETON_ID];
  if (value) return value;
  return getVacationResponse();
}