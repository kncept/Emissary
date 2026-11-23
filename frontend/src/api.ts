import type { AuthState, LoginResponse } from './types.ts';
import { login as generatedLogin } from './api/generated/auth.ts';
import type { TokenResponse, Error as AuthError } from './api/generated/schemas/auth/index.ts';
import {
  authenticatedFetch,
  clearTokens as clearToken,
  getToken,
  setTokens,
} from './api/auth-fetch.ts';
import { resetJMAPClientCache } from './api/jmap-client.ts';
import type {
  BucketRootResponse,
  ConfigurationDetail,
  ConfigurationsResponse,
  ProcessEmailResponse,
  ReceiverIdentitiesResponse,
  ReceiverRulesRequest,
  ReceiverRulesResponse,
} from './api/generated/schemas/support/index.ts';
import {
  MASQUERADE_AS_KEY,
  getEmails,
  getEmail,
  getRawEmail,
  markRead,
  markFlagged,
  deleteEmail,
  deleteEmails,
  moveEmailsToTrash,
  parseAddressList,
  getVacationResponse,
  setVacationResponse,
  sendEmail as jmapSendEmail,
} from './api/jmap-client.ts';

// Preserve the existing public helper for callers that clear the session.
export { clearToken };

const API_BASE = '/api';

export async function getUsers(cursor?: string, pageSize = 20) {
  const params = new URLSearchParams();
  if (cursor) params.set('cursor', cursor);
  if (pageSize) params.set('pageSize', String(pageSize));
  const qs = params.toString();
  const response = await authenticatedFetch(`${API_BASE}/user/${qs ? `?${qs}` : ''}`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to load users');
  }
  return data as { users: Array<{ email: string; enabled: boolean; systemManager: boolean; userManager: boolean; rules: string[] }>; nextCursor?: string };
}

export async function autocompleteUsers(q: string): Promise<{ users: string[] }> {
  const params = new URLSearchParams({ q });
  const response = await authenticatedFetch(`${API_BASE}/user/autocomplete?${params.toString()}`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to search users');
  }
  return data as { users: string[] };
}

export async function getUser(email: string) {
  const response = await authenticatedFetch(`${API_BASE}/user/${encodeURIComponent(email)}`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to load user');
  }
  return data as { email: string; enabled: boolean; systemManager: boolean; userManager: boolean; rules: string[] };
}

export async function createUser(body: { email: string; password?: string; enabled?: boolean; systemManager?: boolean; userManager?: boolean; rules?: string[] }) {
  const response = await authenticatedFetch(`${API_BASE}/user/`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to create user');
  }
  return data;
}

export async function updateUser(email: string, body: { enabled?: boolean; systemManager?: boolean; userManager?: boolean; rules?: string[] }) {
  const response = await authenticatedFetch(`${API_BASE}/user/${encodeURIComponent(email)}`, {
    method: 'PATCH',
    body: JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to update user');
  }
  return data;
}

export async function deleteUser(email: string) {
  const response = await authenticatedFetch(`${API_BASE}/user/${encodeURIComponent(email)}`, {
    method: 'DELETE',
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to delete user');
  }
  return data;
}

export async function changeUserPassword(email: string, body: { oldPassword?: string; newPassword: string }) {
  const response = await authenticatedFetch(`${API_BASE}/user/${encodeURIComponent(email)}/password`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Failed to change password');
  }
  return data;
}

// Cached in memory only: the capability is fixed for the lifetime of the
// deployed backend, and the client must never make this request more than
// once. A rejected promise is cached too, so a failed check is not retried.
let passwordChangeSupport: Promise<boolean> | undefined;

export function isPasswordChangeSupported(): Promise<boolean> {
  if (!passwordChangeSupport) {
    passwordChangeSupport = (async () => {
      const response = await authenticatedFetch(`${API_BASE}/user/password-change-supported`);
      if (!response.ok) {
        const data = await response.json().catch(() => ({}));
        throw new Error((data as { error?: string }).error || 'Failed to check password change support');
      }
      const data = await response.json() as { supported?: boolean };
      return data.supported === true;
    })();
  }
  return passwordChangeSupport;
}

export async function changeOwnPassword(body: { oldPassword?: string; newPassword: string }) {
  const response = await authenticatedFetch(`${API_BASE}/user/change-password`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 403 || response.status === 404) {
      throw new Error('Password changes are not supported by the configured user backend.');
    }
    throw new Error((data as { error?: string }).error || 'Failed to change password');
  }
  return data;
}

export async function getSupportConfigurations(): Promise<ConfigurationsResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/configurations`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to load configurations');
  }
  return data as ConfigurationsResponse;
}

export async function getSupportConfiguration(name: string): Promise<ConfigurationDetail> {
  const response = await authenticatedFetch(`${API_BASE}/support/configurations/${encodeURIComponent(name)}`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to load configuration');
  }
  return data as ConfigurationDetail;
}

export async function getSupportBucketRoot(): Promise<BucketRootResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/bucket/root`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to list the bucket root');
  }
  return data as BucketRootResponse;
}

export async function processSupportEmail(id: string): Promise<ProcessEmailResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/process-email`, {
    method: 'POST',
    body: JSON.stringify({ id }),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to process email');
  }
  return data as ProcessEmailResponse;
}

export async function getSupportReceiverIdentities(): Promise<ReceiverIdentitiesResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/receiver/identities`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to load receiver identities');
  }
  return data as ReceiverIdentitiesResponse;
}

export async function getSupportReceiverRules(): Promise<ReceiverRulesResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/receiver/rules`);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to load receiver rules');
  }
  return data as ReceiverRulesResponse;
}

export async function saveSupportReceiverRules(rules: ReceiverRulesRequest['rules']): Promise<ReceiverRulesResponse> {
  const response = await authenticatedFetch(`${API_BASE}/support/receiver/rules`, {
    method: 'PUT',
    body: JSON.stringify({ rules }),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to save receiver rules');
  }
  return data as ReceiverRulesResponse;
}

export async function deleteSupportReceiverRule(identity: string): Promise<ReceiverRulesResponse> {
  const response = await authenticatedFetch(
    `${API_BASE}/support/receiver/rules/${encodeURIComponent(identity)}`,
    { method: 'DELETE' },
  );
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || 'Failed to delete receiver rules');
  }
  return data as ReceiverRulesResponse;
}

export async function checkAuth(): Promise<AuthState> {
  const token = getToken();
  if (!token) {
    return { authenticated: false };
  }

  // Parse JWT claims from the token (username, systemManager, emissaryUserManagement)
  let username = '';
  let systemManager = false;
  let emissaryUserManagement = false;
  try {
    const payload = JSON.parse(atob(token.split('.')[1]));
    username = payload.sub || '';
    systemManager = payload.SystemManager === true || payload.systemManager === true;
    emissaryUserManagement = payload.EmissaryUserManagement === true || payload.emissaryUserManagement === true;
  } catch {
    // Ignore parse errors
  }

  // An unparseable token (or one without a subject) means no session.
  // A stale but well-formed token is fine here: the first authenticated
  // request triggers a refresh, and a fully expired pair redirects to login.
  if (!username) {
    return { authenticated: false };
  }

  return { authenticated: true, username, systemManager, emissaryUserManagement };
}

export async function login(username: string, password: string): Promise<LoginResponse> {
  const response = await generatedLogin({ username, password });
  if (response.status !== 200 || !response.data) {
    clearToken();
    const errorData = response.data as AuthError | undefined;
    return { error: errorData?.error || 'Login failed' };
  }

  const tokens = response.data as TokenResponse;
  setTokens(tokens.accessToken, tokens.refreshToken);

  // Invalidate JMAP client cache on new login so the new session's
  // JMAP session and mailbox ids are fetched fresh.
  resetJMAPClientCache();

  // A masquerade target left over from a previous session would be used as
  // the accountId for the first JMAP email requests after navigating home.
  // Clear it before the app can make any of those requests.
  window.localStorage.removeItem(MASQUERADE_AS_KEY);

  // Parse username from access token (JWT)
  let usernameFromToken = username;
  let systemManager = false;
  let emissaryUserManagement = false;
  try {
    const payload = JSON.parse(atob(tokens.accessToken.split('.')[1]));
    usernameFromToken = payload.sub || username;
    systemManager = payload.SystemManager === true || payload.systemManager === true;
    emissaryUserManagement = payload.EmissaryUserManagement === true || payload.emissaryUserManagement === true;
  } catch {
    // Ignore parse errors
  }

  return {
    token: tokens.accessToken,
    tokenType: tokens.tokenType,
    expiresAt: tokens.expiresAt,
    username: usernameFromToken,
    email: usernameFromToken,
    systemManager,
    emissaryUserManagement,
  };
}

export async function logout(): Promise<void> {
  try {
    const token = getToken();
    if (token) {
      await authenticatedFetch(`${API_BASE}/auth/logout`, { method: 'POST' });
    }
  } finally {
    clearToken();
    resetJMAPClientCache();
  }
}

// Re-export email functions from JMAP client
export {
  MASQUERADE_AS_KEY,
  getEmails,
  getEmail,
  getRawEmail,
  markRead,
  markFlagged,
  deleteEmail,
  deleteEmails,
  moveEmailsToTrash,
  parseAddressList,
  getVacationResponse,
  setVacationResponse,
  jmapSendEmail as sendEmail,
};
