import { afterEach, describe, expect, it, vi } from 'vitest';
import { checkAuth } from './api';

describe('checkAuth', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns the authentication state reported by the server', async () => {
    const fetchMock = vi.fn(async () => ({
      status: 200,
      json: async () => ({ authenticated: true, username: 'Emissary' }),
    }));
    vi.stubGlobal('fetch', fetchMock);

    const result = await checkAuth();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith('/api/auth/check', { credentials: 'include' });
    expect(result).toEqual({ authenticated: true, username: 'Emissary' });
  });
});
