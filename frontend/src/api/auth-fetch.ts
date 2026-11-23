import { refresh as generatedRefresh } from './generated/auth.ts';
import type { TokenResponse } from './generated/schemas/auth/index.ts';

const TOKEN_STORAGE_KEY = 'emissary_token';
const REFRESH_TOKEN_KEY = 'emissary_refresh_token';

export function getToken(): string | null {
  return window.localStorage.getItem(TOKEN_STORAGE_KEY);
}

export function setTokens(accessToken: string, refreshToken: string): void {
  window.localStorage.setItem(TOKEN_STORAGE_KEY, accessToken);
  window.localStorage.setItem(REFRESH_TOKEN_KEY, refreshToken);
}

export function clearTokens(): void {
  window.localStorage.removeItem(TOKEN_STORAGE_KEY);
  window.localStorage.removeItem(REFRESH_TOKEN_KEY);
}

/**
 * Extracts the session identity from the current access token.
 * Returns a string key combining the JWT `sub` (username) and `sid` (session id).
 * This is used to scope JMAP caches to the current user session.
 */
export function getSessionIdentity(): string | null {
  const token = getToken();
  if (!token) return null;
  try {
    const payload = JSON.parse(atob(token.split('.')[1]));
    const sub = payload.sub || '';
    const sid = payload.sid || '';
    if (!sub) return null;
    return `${sub}|${sid}`;
  } catch {
    return null;
  }
}

// A refresh rotates the refresh token (strict issue stepping on the server),
// so concurrent 401s must share a single refresh rather than racing to use
// the same already-spent token.
let refreshPromise: Promise<boolean> | null = null;

async function performRefresh(): Promise<boolean> {
  const refreshToken = window.localStorage.getItem(REFRESH_TOKEN_KEY);
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
    // Network failure or an invalid/expired refresh token: fall through to logout.
  }
  return false;
}

function refreshAccessToken(): Promise<boolean> {
  if (!refreshPromise) {
    refreshPromise = performRefresh().finally(() => {
      refreshPromise = null;
    });
  }
  return refreshPromise;
}

/**
 * Fetches with the current access token. On a 401 it refreshes the token once
 * (sharing any in-flight refresh), retries, and only then logs out. Both the
 * REST and JMAP clients go through here so their session handling stays the
 * same.
 */
export async function authenticatedFetch(url: string, options: RequestInit = {}): Promise<Response> {
  const token = getToken();
  const headers = new Headers(options.headers);
  if (!headers.has('Content-Type') && options.body != null) {
    headers.set('Content-Type', 'application/json');
  }
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  // Use a fresh Headers object per attempt so a retry never mutates headers
  // already handed to fetch.
  const send = (bearerToken: string | null) => {
    const requestHeaders = new Headers(headers);
    if (bearerToken) {
      requestHeaders.set('Authorization', `Bearer ${bearerToken}`);
    }
    return fetch(url, { ...options, headers: requestHeaders, cache: 'no-store' });
  };
  let response = await send(token);

  if (response.status === 401) {
    const currentToken = getToken();
    if (currentToken && currentToken !== token) {
      // Another request already refreshed the token while this one was in
      // flight. Retry with that token instead of rotating again.
      response = await send(currentToken);
    } else if (await refreshAccessToken()) {
      response = await send(getToken());
    }

    if (response.status === 401) {
      clearTokens();
      window.location.href = '/login';
      throw new Error('Unauthorized');
    }
  }

  return response;
}
