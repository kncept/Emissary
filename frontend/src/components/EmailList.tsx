import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { formatDistanceToNow, parseISO } from 'date-fns';
import { getEmails, deleteEmail, markRead, markFlagged } from '../api.ts';
import type { Email } from '../types.ts';
import ConfirmDialog from './ConfirmDialog.tsx';

export default function EmailList() {
  const [emails, setEmails] = useState<Email[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    loadEmails();
  }, []);

  const loadEmails = async () => {
    try {
      setLoading(true);
      setError('');
      // The JMAP client selects the account (including admin masquerade)
      // through accountId; there is no separate user filter.
      const data = await getEmails();
      setEmails(data);
    } catch {
      setError('Failed to load emails');
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteId) return;
    setDeleting(true);
    try {
      await deleteEmail(deleteId);
      setEmails((prev) => prev.filter((email) => email.id !== deleteId));
      setDeleteId(null);
    } catch {
      setActionError('Failed to delete email');
      setDeleteId(null);
    } finally {
      setDeleting(false);
    }
  };

  const updateEmail = (id: string, changes: Partial<Email>) => {
    setEmails((prev) => prev.map((email) => (email.id === id ? { ...email, ...changes } : email)));
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

  if (loading) {
    return (
      <div className="email-list-loading">
        <div className="spinner"></div>
      </div>
    );
  }

  if (error) {
    return <div className="error-message">{error}</div>;
  }

  return (
    <div className="email-list">
      <div className="email-list-header">
        <h2>Inbox ({emails.length})</h2>
        <Link to="/compose" className="btn btn-primary">
          Compose
        </Link>
      </div>
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
      ) : (
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