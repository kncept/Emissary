// Inbox polling is driven by the last time emails were actually checked, not
// by when the inbox was mounted. Every call that checks emails (getEmails,
// including the polling check itself) reports here so subscribers can restart
// their timer. This keeps a manual refresh or a refresh from another view from
// being followed by a redundant poll five minutes later.

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

// Navigating to the inbox while already there does not remount the view, so
// the nav needs an explicit way to ask the mounted inbox to check again.
type EmailRefreshRequestListener = () => void;

const refreshListeners = new Set<EmailRefreshRequestListener>();
let refreshPending = false;

export function requestEmailRefresh(): void {
  if (refreshListeners.size === 0) {
    // The inbox is not mounted (e.g. the user clicked Inbox from the email
    // view); remember the request so it can be honoured on mount.
    refreshPending = true;
  }
  for (const listener of [...refreshListeners]) {
    listener();
  }
}

// Read a pending refresh request without consuming it. Lets a mounting inbox
// initialize its loading state before the effect consumes the request.
export function hasEmailRefreshRequest(): boolean {
  return refreshPending;
}

// Consume a pending refresh request. Returns true once for each request that
// has not yet been handled by a mounted inbox.
export function consumeEmailRefreshRequest(): boolean {
  if (!refreshPending) return false;
  refreshPending = false;
  return true;
}

// Clear any pending refresh request (used by tests between independent mounts).
export function clearEmailRefreshRequest(): void {
  refreshPending = false;
}

export function onEmailRefreshRequest(listener: EmailRefreshRequestListener): () => void {
  refreshListeners.add(listener);
  return () => {
    refreshListeners.delete(listener);
  };
}
