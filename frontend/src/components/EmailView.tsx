import { useEffect, useState } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { parseISO, format } from 'date-fns';
import { getEmail, markRead, markFlagged, deleteEmail } from '../api';
import type { Email } from '../types';

export default function EmailView() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [email, setEmail] = useState<Email | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    if (id) {
      loadEmail();
    }
  }, [id]);

  const loadEmail = async () => {
    try {
      setLoading(true);
      const data = await getEmail(id!);
      setEmail(data);
    } catch (err) {
      setError('Failed to load email');
    } finally {
      setLoading(false);
    }
  };

  const handleMarkRead = async (read: boolean) => {
    if (!email) return;
    try {
      await markRead(email.id, read);
      setEmail({ ...email, read });
    } catch (err) {
      // Error handling
    }
  };

  const handleMarkFlagged = async (flagged: boolean) => {
    if (!email) return;
    try {
      await markFlagged(email.id, flagged);
      setEmail({ ...email, flagged });
    } catch (err) {
      // Error handling
    }
  };

  const handleDelete = async () => {
    if (!email) return;
    if (!window.confirm('Delete this email? This cannot be undone.')) return;
    try {
      await deleteEmail(email.id);
      navigate('/');
    } catch {
      setError('Failed to delete email');
    }
  };

  const formatDate = (dateStr: string) => {
    try {
      return format(parseISO(dateStr), 'PPpp');
    } catch {
      return dateStr;
    }
  };

  if (loading) {
    return (
      <div className="email-view-loading">
        <div className="spinner"></div>
      </div>
    );
  }

  if (error || !email) {
    return (
      <div className="email-view-error">
        <p>{error || 'Email not found'}</p>
        <Link to="/" className="btn btn-secondary">
          Back to Inbox
        </Link>
      </div>
    );
  }

  return (
    <article className="email-view">
      <header className="email-header">
        <div className="email-header-main">
          <h1 className="email-subject">{email.subject || '(No subject)'}</h1>
          <div className="email-meta-row">
            <span className="email-from">
              <strong>From:</strong> {email.from}
            </span>
            <span className="email-to">
              <strong>To:</strong> {email.to}
            </span>
            <time className="email-date" dateTime={email.timestamp}>
              {formatDate(email.timestamp)}
            </time>
          </div>
        </div>
        <div className="email-actions">
          <button
            className={`btn ${email.read ? 'btn-secondary' : 'btn-primary'}`}
            onClick={() => handleMarkRead(!email.read)}
          >
            {email.read ? 'Mark Unread' : 'Mark Read'}
          </button>
          <button
            className={`btn ${email.flagged ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => handleMarkFlagged(!email.flagged)}
          >
            {email.flagged ? 'Unflag' : 'Flag'}
          </button>
          <Link to={`/compose/${email.id}`} className="btn btn-secondary">
            Reply
          </Link>
          <button className="btn btn-danger" onClick={handleDelete}>
            Delete
          </button>
          <Link to="/" className="btn btn-secondary">
            Back
          </Link>
        </div>
      </header>
      <div className="email-body">
        <pre>{email.body}</pre>
      </div>
    </article>
  );
}