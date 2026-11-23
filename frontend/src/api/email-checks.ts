// Mailbox polling is driven by the last time emails were actually checked, not
// by when a folder was mounted. Every call that checks emails (getEmails,
// including the polling check itself) reports here so subscribers can restart
// their timer. This keeps a manual refresh or a refresh from another view from
// being followed by a redundant poll five minutes later.

import type { MailboxRole } from '../types.ts';

export const EMAIL_CHECK_INTERVAL_MS = 5 * 60 * 1000;

type EmailCheckListener = () => void;

let lastCheckedAt: number | null = null;
const listeners = new Set<EmailCheckListener>();

export function getLastEmailCheckAt(): number | null {
  return lastCheckedAt;
}

export function recordEmailCheck(): void {
  lastCheckedAt = Date.now();
  // Copy before iterating so a listener that unsubscribes cannot skip another.
  for (const listener of [...listeners]) {
    listener();
  }
}

export function onEmailCheck(listener: EmailCheckListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// Navigating to the folder you are already in does not remount the view, so
// the sidebar needs an explicit way to ask that mounted folder to check again.
// Requests are scoped by role: refreshing Sent must not refetch the Inbox.
type EmailRefreshRequestListener = (role: MailboxRole) => void;

interface RefreshSubscription {
  role: MailboxRole;
  listener: EmailRefreshRequestListener;
}

const refreshListeners = new Set<RefreshSubscription>();
const pendingRefreshes = new Set<MailboxRole>();

export function requestEmailRefresh(role: MailboxRole = 'inbox'): void {
  const subscriptions = [...refreshListeners].filter((entry) => entry.role === role);
  // Only remember the request when no mounted folder can handle it now;
  // otherwise a refresh would be replayed on the next mount.
  if (subscriptions.length === 0) pendingRefreshes.add(role);
  for (const subscription of subscriptions) {
    subscription.listener(role);
  }
}

// Read a pending refresh request without consuming it. Lets a mounting folder
// initialize its loading state before the effect consumes the request.
export function hasEmailRefreshRequest(role: MailboxRole = 'inbox'): boolean {
  return pendingRefreshes.has(role);
}

// Consume a pending refresh request. Returns true once for each request that
// has not yet been handled by a mounted folder.
export function consumeEmailRefreshRequest(role: MailboxRole = 'inbox'): boolean {
  if (!pendingRefreshes.delete(role)) return false;
  return true;
}

// Clear pending requests (all folders, or one) between independent test mounts.
export function clearEmailRefreshRequest(role?: MailboxRole): void {
  if (role) pendingRefreshes.delete(role);
  else pendingRefreshes.clear();
}

export function onEmailRefreshRequest(
  listener: EmailRefreshRequestListener,
  role: MailboxRole = 'inbox',
): () => void {
  const subscription: RefreshSubscription = { role, listener };
  refreshListeners.add(subscription);
  return () => {
    refreshListeners.delete(subscription);
  };
}
