import { Email, SendEmailRequest } from './types';

const API_BASE = '/api';

async function fetchWithAuth(url: string, options: RequestInit = {}) {
  const response = await fetch(url, {
    ...options,
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  });

  if (response.status === 401) {
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  return response;
}

export async function checkAuth(): Promise<{ authenticated: boolean; username?: string }> {
  const response = await fetch(`${API_BASE}/auth/check`, { credentials: 'include' });
  return response.json();
}

export async function login(username: string, password: string): Promise<{ success: boolean; error?: string }> {
  const response = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });
  return response.json();
}

export async function logout(): Promise<void> {
  await fetch(`${API_BASE}/auth/logout`, {
    method: 'POST',
    credentials: 'include',
  });
}

export async function getEmails(): Promise<Email[]> {
  const response = await fetchWithAuth(`${API_BASE}/emails`);
  return response.json();
}

export async function getEmail(id: string): Promise<Email> {
  const response = await fetchWithAuth(`${API_BASE}/emails/${id}`);
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

export async function sendEmail(data: SendEmailRequest): Promise<{ success: boolean; message?: string }> {
  const response = await fetchWithAuth(`${API_BASE}/emails/send`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
  return response.json();
}