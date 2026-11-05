import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { formatDistanceToNow, parseISO } from 'date-fns';
import { getEmails } from '../api';
import { Email } from '../types';

export default function EmailList() {
  const [emails, setEmails] = useState<Email[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    loadEmails();
  }, []);

  const loadEmails = async () => {
    try {
      setLoading(true);
      const data = await getEmails();
      setEmails(data);
    } catch (err) {
      setError('Failed to load emails');
    } finally {
      setLoading(false);
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
              <Link to={`/email/${email.id}`} className="email-link">
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
                    // Toggle read status via API would go here
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
                  }}
                  aria-label={email.flagged ? 'Remove flag' : 'Flag'}
                >
                  {email.flagged ? '🚩' : '🏳️'}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}