import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { checkAuth, deleteEmail, login } from './api';

const TOKEN_KEY = 'emissary_token';

describe('auth api', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('checkAuth', () => {
    it('sends no Authorization header when no token is stored', async () => {
      const fetchMock = vi.fn(async () => ({
        status: 200,
        ok: true,
        json: async () => ({ authenticated: false }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock).toHaveBeenCalledWith('/api/auth/check', { headers: {} });
      expect(result).toEqual({ authenticated: false });
    });

    it('sends the stored token as a Bearer header', async () => {
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async () => ({
        status: 200,
        ok: true,
        json: async () => ({ authenticated: true, username: 'Emissary' }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      const result = await checkAuth();

      expect(fetchMock).toHaveBeenCalledWith('/api/auth/check', {
        headers: { Authorization: 'Bearer abc123' },
      });
      expect(result).toEqual({ authenticated: true, username: 'Emissary' });
    });
  });

  describe('deleteEmail', () => {
    it('sends a DELETE request with the stored token', async () => {
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async () => ({
        status: 200,
        ok: true,
        json: async () => ({ success: true }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      await deleteEmail('email-id');

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock).toHaveBeenCalledWith('/api/emails/email-id', {
        method: 'DELETE',
        headers: { Authorization: 'Bearer abc123', 'Content-Type': 'application/json' },
      });
    });

    it('throws when the delete fails', async () => {
      window.localStorage.setItem(TOKEN_KEY, 'abc123');
      const fetchMock = vi.fn(async () => ({
        status: 404,
        ok: false,
        json: async () => ({ error: 'email not found' }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      await expect(deleteEmail('missing')).rejects.toThrow('Failed to delete email (404)');
    });
  });

  describe('login', () => {
    it('stores the returned token and reports it to the caller', async () => {
      const fetchMock = vi.fn(async () => ({
        status: 200,
        ok: true,
        json: async () => ({ token: 'jwt-token', tokenType: 'Bearer', expiresAt: '2026-01-01T00:00:00Z' }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      const result = await login('user', 'pass');

      expect(result.token).toBe('jwt-token');
      expect(window.localStorage.getItem(TOKEN_KEY)).toBe('jwt-token');
    });

    it('does not store a token and surfaces the error on failure', async () => {
      const fetchMock = vi.fn(async () => ({
        status: 401,
        ok: false,
        json: async () => ({ error: 'invalid credentials' }),
      }));
      vi.stubGlobal('fetch', fetchMock);

      const result = await login('user', 'wrong');

      expect(result.token).toBeUndefined();
      expect(result.error).toBe('invalid credentials');
      expect(window.localStorage.getItem(TOKEN_KEY)).toBeNull();
    });
  });
});
