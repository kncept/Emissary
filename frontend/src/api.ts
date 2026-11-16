import type { AuthState, LoginResponse } from './types.ts';
import { login as generatedLogin, refresh as generatedRefresh } from './api/generated/auth.ts';
import type { TokenResponse, Error as AuthError } from './api/generated/schemas/auth/index.ts';
import {
  getEmails,
  getEmail,
  markRead,
  markFlagged,
  deleteEmail,
  sendEmail as jmapSendEmail,
} from './api/jmap-client.ts';

const API_BASE = '/api';
const TOKEN_STORAGE_KEY = 'emissary_token';
const REFRESH_TOKEN_KEY = 'emissary_refresh_token';

function getToken(): string | null {
  return window.localStorage.getItem(TOKEN_STORAGE_KEY);
}

function getRefreshToken(): string | null {
  return window.localStorage.getItem(REFRESH_TOKEN_KEY);
}

function setTokens(accessToken: string, refreshToken: string): void {
  window.localStorage.setItem(TOKEN_STORAGE_KEY, accessToken);
  window.localStorage.setItem(REFRESH_TOKEN_KEY, refreshToken);
}

export function clearToken(): void {
  window.localStorage.removeItem(TOKEN_STORAGE_KEY);
  window.localStorage.removeItem(REFRESH_TOKEN_KEY);
}

async function refreshAccessToken(): Promise<boolean> {
  const refreshToken = getRefreshToken();
  if (!refreshToken) {
    return false;
  }

  try {
    const response = await generatedRefresh({ refreshToken });
    if (response.status === 200 && response.data) {
      const tokens = response.data as TokenResponse;
      setTokens(tokens.accessToken, tokens.refreshToken);
      return true;
    }
  } catch {
    // Refresh failed
  }
  return false;
}

async function authenticatedFetch(url: string, options: RequestInit = {}): Promise<Response> {
  const token = getToken();
  const headers = new Headers(options.headers);
  headers.set('Content-Type', 'application/json');
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  let response = await fetch(url, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    // Try to refresh the token
    const refreshed = await refreshAccessToken();
    if (refreshed) {
      // Retry with new token
      const newToken = getToken();
      headers.set('Authorization', `Bearer ${newToken}`);
      response = await fetch(url, {
        ...options,
        headers,
      });
    }

    if (response.status === 401) {
      // Still unauthorized, clear tokens and redirect
      clearToken();
      window.location.href = '/login';
      throw new Error('Unauthorized');
    }
  }

  return response;
}

export async function checkAuth(): Promise<AuthState> {
  const token = getToken();
  if (!token) {
    return { authenticated: false };
  }

  try {
    const response = await authenticatedFetch(`${API_BASE}/auth/check`);
    if (!response.ok) {
      return { authenticated: false };
    }
    const data = await response.json();
    return data;
  } catch {
    return { authenticated: false };
  }
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

  // Parse username from access token (JWT)
  let usernameFromToken = username;
  let admin = false;
  try {
    const payload = JSON.parse(atob(tokens.accessToken.split('.')[1]));
    usernameFromToken = payload.sub || username;
    admin = payload.Admin === true || payload.admin === true;
  } catch {
    // Ignore parse errors
  }

  return {
    token: tokens.accessToken,
    tokenType: tokens.tokenType,
    expiresAt: tokens.expiresAt,
    username: usernameFromToken,
    email: usernameFromToken,
    admin,
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
  }
}

// Re-export email functions from JMAP client
export { getEmails, getEmail, markRead, markFlagged, deleteEmail, jmapSendEmail as sendEmail };