const TOKEN_STORAGE_KEY = 'emissary_token';

function getToken(): string | null {
  return window.localStorage.getItem(TOKEN_STORAGE_KEY);
}

export function setToken(token: string): void {
  window.localStorage.setItem(TOKEN_STORAGE_KEY, token);
}

export function clearToken(): void {
  window.localStorage.removeItem(TOKEN_STORAGE_KEY);
}

export interface FetchResponse<T> {
  data: T;
  status: number;
  headers: Headers;
}

export async function customFetch<T = unknown>(
  url: string,
  options: RequestInit = {}
): Promise<FetchResponse<T>> {
  const token = getToken();
  const headers = new Headers(options.headers);
  headers.set('Content-Type', 'application/json');
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    clearToken();
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  const data = await response.json();
  return {
    data,
    status: response.status,
    headers: response.headers,
  } as FetchResponse<T>;
}