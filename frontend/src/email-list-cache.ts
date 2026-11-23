import type { Email } from './types.ts';

// Module-level cache shared by inbox mounts. Kept out of the component so the
// component module only exports React components (needed for Fast Refresh).
let cachedEmails: Email[] | null = null;

export function getCachedEmails(): Email[] | null {
  return cachedEmails;
}

export function setCachedEmails(emails: Email[]): void {
  cachedEmails = emails;
}

// Reset the cache between independent mounts in tests.
export function resetEmailListCache(): void {
  cachedEmails = null;
}
