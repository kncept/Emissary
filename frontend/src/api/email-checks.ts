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

export function requestEmailRefresh(): void {
  for (const listener of [...refreshListeners]) {
    listener();
  }
}

export function onEmailRefreshRequest(listener: EmailRefreshRequestListener): () => void {
  refreshListeners.add(listener);
  return () => {
    refreshListeners.delete(listener);
  };
}
