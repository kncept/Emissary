import type { Email, MailboxRole } from './types.ts';

// Module-level cache shared by mailbox mounts. Kept out of the component so
// the component module only exports React components (needed for Fast Refresh).
// Each folder keeps its own list so switching between Inbox, Drafts and Sent
// never replaces another folder's cached messages.
const cachedEmails = new Map<MailboxRole, Email[]>();

export function getCachedEmails(role: MailboxRole = 'inbox'): Email[] | null {
  return cachedEmails.get(role) ?? null;
}

export function setCachedEmails(emails: Email[], role: MailboxRole = 'inbox'): void {
  cachedEmails.set(role, emails);
}

// Reset every folder (or just one) between independent mounts in tests.
export function resetEmailListCache(role?: MailboxRole): void {
  if (role) cachedEmails.delete(role);
  else cachedEmails.clear();
}
