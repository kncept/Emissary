import type { AuthState, Email, LoginResponse, SendEmailRequest } from './types';

const API_BASE = '/api';
const TOKEN_STORAGE_KEY = 'emissary_token';

// Authentication is cookie-free: the JWT is kept in localStorage and sent to
// the API as a Bearer token on every request. See the note in TODO.md.
function getToken(): string | null {
  return window.localStorage.getItem(TOKEN_STORAGE_KEY);
}

function setToken(token: string): void {
  window.localStorage.setItem(TOKEN_STORAGE_KEY, token);
}

export function clearToken(): void {
  window.localStorage.removeItem(TOKEN_STORAGE_KEY);
}

function authHeaders(): Record<string, string> {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function fetchWithAuth(url: string, options: RequestInit = {}) {
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders(),
      ...options.headers,
    },
  });

  if (response.status === 401) {
    // The token is missing, invalid or expired: drop it and go to login.
    clearToken();
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  return response;
}

export async function checkAuth(): Promise<AuthState> {
  const response = await fetch(`${API_BASE}/auth/check`, { headers: authHeaders() });
  if (!response.ok) {
    return { authenticated: false };
  }
  return response.json();
}

export async function login(username: string, password: string): Promise<LoginResponse> {
  const response = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });
  const data: LoginResponse = await response.json();
  if (!response.ok || !data.token) {
    clearToken();
    return { error: data.error || 'Login failed' };
  }
  setToken(data.token);
  return data;
}

export async function logout(): Promise<void> {
  try {
    // JWTs are stateless, so the backend endpoint is just a hook; the real
    // work is discarding the token client-side.
    await fetch(`${API_BASE}/auth/logout`, {
      method: 'POST',
      headers: authHeaders(),
    });
  } finally {
    clearToken();
  }
}

export async function getEmails(user?: string): Promise<Email[]> {
  const query = user?.trim() ? `?user=${encodeURIComponent(user.trim())}` : '';
  const response = await fetchWithAuth(`${API_BASE}/emails${query}`);
  if (!response.ok) {
    throw new Error(`Failed to list emails (${response.status})`);
  }
  const data = await response.json();
  return Array.isArray(data) ? data : [];
}

export async function getEmail(id: string): Promise<Email> {
  const response = await fetchWithAuth(`${API_BASE}/emails/${id}`);
  if (!response.ok) {
    throw new Error(`Failed to load email (${response.status})`);
  }
  return response.json();
}

export async function markRead(id: string, read: boolean): Promise<void> {
  await fetchWithAuth(`${API_BASE}/emails/${id}/read`, {
    method: 'POST',
    body: JSON.stringify({ read }),
  });
}

export async function markFlagged(id: string, flagged: boolean): Promise<void> {
  await fetchWithAuth(`${API_BASE}/emails/${id}/flag`, {
    method: 'POST',
    body: JSON.stringify({ flagged }),
  });
}

// Deleting removes the message for every recipient, not just the caller.
export async function deleteEmail(id: string): Promise<void> {
  const response = await fetchWithAuth(`${API_BASE}/emails/${id}`, { method: 'DELETE' });
  if (!response.ok) {
    throw new Error(`Failed to delete email (${response.status})`);
  }
}

export async function sendEmail(data: SendEmailRequest): Promise<{ success: boolean; message?: string }> {
  const response = await fetchWithAuth(`${API_BASE}/emails/send`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
  return response.json();
}
