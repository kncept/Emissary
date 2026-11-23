import { describe, expect, it, vi } from 'vitest';

// Importing jsdom in browser code drags Node-only modules (and undici's
// SharedArrayBuffer checks) into the production bundle. Fail loudly if it
// ever creeps back into the sanitizer.
vi.mock('jsdom', () => {
  throw new Error('jsdom must not be imported by browser code');
});

describe('browser-safe sanitizer', () => {
  it('loads and sanitizes without jsdom', async () => {
    const { sanitizeEmailHtml } = await import('./sanitize.ts');
    expect(sanitizeEmailHtml('<p>ok</p>')).toBe('<p>ok</p>');
  });
});
