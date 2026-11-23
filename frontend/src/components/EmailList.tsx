import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { formatDistanceToNow, parseISO } from 'date-fns';
import { getEmails, deleteEmail, markRead, markFlagged } from '../api.ts';
import {
  EMAIL_CHECK_INTERVAL_MS,
  consumeEmailRefreshRequest,
  hasEmailRefreshRequest,
  onEmailCheck,
  onEmailRefreshRequest,
} from '../api/email-checks.ts';
import { getCachedEmails, setCachedEmails } from '../email-list-cache.ts';
import type { Email } from '../types.ts';
import ConfirmDialog from './ConfirmDialog.tsx';

type Density = 'dense' | 'comfortable' | 'spacious';

const DENSITY_KEY = 'emissary.emailList.density';

function getInitialDensity(): Density {
  try {
    const stored = localStorage.getItem(DENSITY_KEY);
    if (stored === 'dense' || stored === 'comfortable' || stored === 'spacious') {
      return stored;
    }
  } catch {
    // ignore
  }
  return 'comfortable';
}

export default function EmailList() {
  const [emails, setEmails] = useState<Email[]>(getCachedEmails() ?? []);
  const [loading, setLoading] = useState(getCachedEmails() === null || hasEmailRefreshRequest());
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [density, setDensity] = useState<Density>(getInitialDensity);
  const checkTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const handleDensityChange = (newDensity: Density) => {
    setDensity(newDensity);
    try {
      localStorage.setItem(DENSITY_KEY, newDensity);
    } catch {
      // ignore
    }
  };

  const loadEmails = useCallback(async (background = false) => {
    try {
      // The JMAP client selects the account (including admin masquerade)
      // through accountId; there is no separate user filter.
      const data = await getEmails();
      setEmails(data);
      setCachedEmails(data);
      setError('');
    } catch {
      // A failed background check keeps the current inbox visible; it will be
      // retried on the next timer. Initial/full refreshes show the error.
      if (!background) setError('Failed to load emails');
    } finally {
      if (!background) setLoading(false);
    }
  }, []);

  useEffect(() => {
    // getEmails reports every check, so this also restarts the timer when a
    // refresh comes from outside the inbox (e.g. after sending a message).
    const scheduleNextCheck = () => {
      if (checkTimer.current !== null) clearTimeout(checkTimer.current);
      checkTimer.current = setTimeout(() => {
        checkTimer.current = null;
        void loadEmails(true);
      }, EMAIL_CHECK_INTERVAL_MS);
    };

    const unsubscribe = onEmailCheck(scheduleNextCheck);
    const unsubscribeRefreshRequest = onEmailRefreshRequest(() => {
      setLoading(true);
      void loadEmails();
    });

    // Fetch on first mount, or when an explicit Inbox refresh was requested
    // while the inbox was unmounted. Otherwise (e.g. Back from an email)
    // restore the cached list without a fetch; the polling timer keeps it fresh.
    const refreshRequested = consumeEmailRefreshRequest();
    if (getCachedEmails() === null || refreshRequested) {
      // A local async wrapper keeps the effect free of synchronous state
      // updates; loadEmails only updates state after its await.
      const initialize = async () => {
        await loadEmails();
      };
      void initialize();
    }

    return () => {
      unsubscribe();
      unsubscribeRefreshRequest();
      if (checkTimer.current !== null) {
        clearTimeout(checkTimer.current);
        checkTimer.current = null;
      }
    };
  }, [loadEmails]);

  const commitEmails = (next: Email[]) => {
    setCachedEmails(next);
    setEmails(next);
  };

  const handleDelete = async () => {
    if (!deleteId) return;
    setDeleting(true);
    try {
      await deleteEmail(deleteId);
      const next = (getCachedEmails() ?? emails).filter((email) => email.id !== deleteId);
      commitEmails(next);
      setDeleteId(null);
    } catch {
      setActionError('Failed to delete email');
      setDeleteId(null);
    } finally {
      setDeleting(false);
    }
  };

  const updateEmail = (id: string, changes: Partial<Email>) => {
    const next = (getCachedEmails() ?? emails).map((email) =>
      email.id === id ? { ...email, ...changes } : email,
    );
    commitEmails(next);
  };

  const handleToggleRead = async (email: Email) => {
    const read = !email.read;
    updateEmail(email.id, { read });
    setActionError('');
    try {
      await markRead(email.id, read);
    } catch {
      updateEmail(email.id, { read: email.read });
      setActionError('Failed to update read status');
    }
  };

  const handleToggleFlagged = async (email: Email) => {
    const flagged = !email.flagged;
    updateEmail(email.id, { flagged });
    setActionError('');
    try {
      await markFlagged(email.id, flagged);
    } catch {
      updateEmail(email.id, { flagged: email.flagged });
      setActionError('Failed to update flag');
    }
  };

  const formatDate = (dateStr: string) => {
    try {
      const date = parseISO(dateStr);
      return formatDistanceToNow(date, { addSuffix: true });
    } catch {
      return dateStr;
    }
  };

  const getInitials = (name: string) => {
    return name
      .split(' ')
      .map((n) => n[0])
      .join('')
      .toUpperCase()
      .slice(0, 2);
  };

  // Only show a full-page spinner when there is nothing to display. During a
  // refresh of an already-loaded inbox, keep the current emails visible and
  // show a small spinner in the header instead; the list is replaced only once
  // the new response arrives.
  if (loading && emails.length === 0) {
    return (
      <div className="email-list-loading">
        <div className="spinner"></div>
      </div>
    );
  }

  if (error && emails.length === 0) {
    return <div className="error-message">{error}</div>;
  }

  return (
    <div className="email-list">
      <div className="email-list-header">
        <h2>Inbox ({emails.length})</h2>
        <div className="header-actions">
          {loading && emails.length > 0 && <div className="spinner small" aria-label="Refreshing" />}
          <div className="density-selector" role="group" aria-label="Display density">
            <button
              className={`density-btn ${density === 'dense' ? 'active' : ''}`}
              onClick={() => handleDensityChange('dense')}
              title="Dense (compact)"
              aria-pressed={density === 'dense'}
            >
              <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M2 4h12v2H2V4zm0 4h12v2H2V8zm0 4h12v2H2v-2z"/></svg>
            </button>
            <button
              className={`density-btn ${density === 'comfortable' ? 'active' : ''}`}
              onClick={() => handleDensityChange('comfortable')}
              title="Comfortable (default)"
              aria-pressed={density === 'comfortable'}
            >
              <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M2 3h12v3H2V3zm0 5h12v3H2V8zm0 5h12v3H2v-3z"/></svg>
            </button>
            <button
              className={`density-btn ${density === 'spacious' ? 'active' : ''}`}
              onClick={() => handleDensityChange('spacious')}
              title="Spacious (roomy)"
              aria-pressed={density === 'spacious'}
            >
              <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M2 2h12v4H2V2zm0 7h12v4H2V9zm0 7h12v4H2v-4z"/></svg>
            </button>
          </div>
          <Link to="/compose" className="btn btn-primary">
            Compose
          </Link>
        </div>
      </div>
      {error && emails.length > 0 && (
        <div className="error-message" role="alert">{error}</div>
      )}
      {actionError && (
        <div className="error-message" role="alert">{actionError}</div>
      )}
      {emails.length === 0 ? (
        <div className="empty-state">
          <p>No emails yet</p>
          <Link to="/compose" className="btn btn-secondary">
            Send your first email
          </Link>
        </div>
      ) : density === 'spacious' ? (
        <ul className="email-list-items" role="list">
          {emails.map((email) => (
            <li
              key={email.id}
              className={`email-item ${email.read ? 'read' : 'unread'} ${email.flagged ? 'flagged' : ''}`}
            >
              <Link to={`/email/${encodeURIComponent(email.id)}`} className="email-link">
                <div className="email-sender">
                  <span className="sender-avatar">{getInitials(email.from)}</span>
                  <div className="sender-info">
                    <span className="sender-name">{email.from}</span>
                    <span className="email-subject">{email.subject || '(No subject)'}</span>
                  </div>
                </div>
                <div className="email-meta">
                  <time className="email-time" dateTime={email.timestamp}>
                    {formatDate(email.timestamp)}
                  </time>
                  {email.flagged && <span className="flag-icon" title="Flagged">🚩</span>}
                </div>
                <div className="email-preview">{email.body.slice(0, 100)}...</div>
              </Link>
              <div className="email-actions">
                <button
                  className="icon-btn"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    void handleToggleRead(email);
                  }}
                  aria-label={email.read ? 'Mark as unread' : 'Mark as read'}
                >
                  {email.read ? '📭' : '📬'}
                </button>
                <button
                  className="icon-btn"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    void handleToggleFlagged(email);
                  }}
                  aria-label={email.flagged ? 'Remove flag' : 'Flag'}
                >
                  {email.flagged ? '🚩' : '🏳️'}
                </button>
                <button
                  className="icon-btn"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setDeleteId(email.id);
                  }}
                  aria-label="Delete email"
                >
                  🗑️
                </button>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <div className={`email-table density-${density}`} role="list">
          {emails.map((email) => (
            <div
              key={email.id}
              className={`email-row ${email.read ? 'read' : 'unread'} ${email.flagged ? 'flagged' : ''}`}
              role="listitem"
            >
              <Link to={`/email/${encodeURIComponent(email.id)}`} className="email-row-link">
                <div className="email-col checkbox-col">
                  <input type="checkbox" className="row-checkbox" aria-label="Select email" />
                </div>
                <div className="email-col star-col">
                  <button
                    className="icon-btn star-btn"
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      void handleToggleFlagged(email);
                    }}
                    aria-label={email.flagged ? 'Remove star' : 'Add star'}
                    aria-pressed={email.flagged}
                  >
                    {email.flagged ? '★' : '☆'}
                  </button>
                </div>
                {(density === 'dense' || density === 'comfortable') && (
                  <div className="email-col left-actions-col">
                    <button
                      className="icon-btn"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        void handleToggleRead(email);
                      }}
                      aria-label={email.read ? 'Mark as unread' : 'Mark as read'}
                    >
                      {email.read ? '📭' : '📬'}
                    </button>
                    <button
                      className="icon-btn"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setDeleteId(email.id);
                      }}
                      aria-label="Delete email"
                    >
                      🗑️
                    </button>
                  </div>
                )}
                <div className="email-col sender-col">
                  <span className="sender-avatar">{getInitials(email.from)}</span>
                  <span className="sender-name">{email.from}</span>
                  {density === 'dense' && (
                    <span className="dense-subject">{email.subject || '(No subject)'}</span>
                  )}
                  {density !== 'dense' && (
                    <>
                      <span className="comfortable-subject">{email.subject || '(No subject)'}</span>
                      <span className="comfortable-preview">{email.body.slice(0, 100)}...</span>
                    </>
                  )}
                </div>
                <div className="email-col time-col">
                  <time className="email-time" dateTime={email.timestamp}>
                    {formatDate(email.timestamp)}
                  </time>
                </div>
              </Link>
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={deleteId !== null}
        title="Delete email"
        message="Delete this email? This cannot be undone."
        busy={deleting}
        onConfirm={handleDelete}
        onCancel={() => setDeleteId(null)}
      />
    </div>
  );
}