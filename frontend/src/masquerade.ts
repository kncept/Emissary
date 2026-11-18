import { MASQUERADE_AS_KEY } from './api/jmap-client';

/** Store the masquerade target and reload so all JMAP calls use that account. */
export function setMasqueradeTarget(email: string) {
  try {
    window.localStorage.setItem(MASQUERADE_AS_KEY, email);
  } catch {
    // ignore storage errors
  }
  window.location.assign('/');
}

/** Clear the masquerade target and reload back to the caller's own account. */
export function clearMasquerade() {
  try {
    window.localStorage.removeItem(MASQUERADE_AS_KEY);
  } catch {
    // ignore storage errors
  }
  window.location.assign('/');
}
