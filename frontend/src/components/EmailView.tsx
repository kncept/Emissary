import { useEffect, useState } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { parseISO, format } from 'date-fns';
import { getEmail, getRawEmail, markRead, markFlagged, deleteEmail } from '../api.ts';
import type { Email } from '../types.ts';
import ConfirmDialog from './ConfirmDialog.tsx';

// Save the raw message as a .eml file using a temporary object URL. A normal
// link cannot set the Authorization header required by the raw endpoint.
function downloadRawMime(raw: string, id: string): void {
  const basename = id.split('/').pop() || 'email.eml';
  const safeName = basename.replace(/[^A-Za-z0-9._-]/g, '_');
  const filename = safeName.toLowerCase().endsWith('.eml') ? safeName : `${safeName}.eml`;
  const blob = new Blob([raw], { type: 'message/rfc822' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

// Render HTML mail in a sandboxed frame so message styles cannot leak into the
// app and message scripts cannot run.
function emailHtmlDocument(html: string): string {
  return `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><style>body{margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;font-size:14px;line-height:1.6;color:#1e293b;word-wrap:break-word}img{max-width:100%;height:auto}a{color:#2563eb}</style></head><body>${html}</body></html>`;
}

export default function EmailView() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [email, setEmail] = useState<Email | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [showRaw, setShowRaw] = useState(false);
  const [rawCache, setRawCache] = useState<{ id: string; mime: string } | null>(null);
  const [rawLoading, setRawLoading] = useState(false);
  const [rawError, setRawError] = useState('');

  // Delivered ids are full object keys such as
  // users/alice@example.com/inbox/<timestamp>_<ses-id>.eml. Links encode
  // them; older links (or routers that do not decode params) may still
  // contain %2F, so decode only in that case.
  const emailID = (() => {
    if (!id) return '';
    if (!/%2f/i.test(id)) return id;
    try {
      return decodeURIComponent(id);
    } catch {
      return id;
    }
  })();

  useEffect(() => {
    if (emailID) {
      loadEmail();
    }
  }, [emailID]);

  useEffect(() => {
    if (!showRaw) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setShowRaw(false);
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [showRaw]);

  const loadEmail = async () => {
    try {
      setLoading(true);
      const data = await getEmail(emailID);
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
    setDeleting(true);
    try {
      await deleteEmail(email.id);
      navigate('/');
    } catch {
      setError('Failed to delete email');
      setConfirmDelete(false);
    } finally {
      setDeleting(false);
    }
  };

  const loadRawMime = async (): Promise<string> => {
    if (rawCache?.id === email!.id) return rawCache.mime;
    setRawLoading(true);
    setRawError('');
    try {
      const raw = await getRawEmail(email!.id);
      setRawCache({ id: email!.id, mime: raw });
      return raw;
    } catch {
      setRawError('Failed to load raw email MIME');
      return '';
    } finally {
      setRawLoading(false);
    }
  };

  const handleViewRaw = async () => {
    setShowRaw(true);
    await loadRawMime();
  };

  const handleDownloadRaw = async () => {
    const raw = await loadRawMime();
    if (raw) downloadRawMime(raw, email!.id);
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
          <Link to={`/compose/${encodeURIComponent(email.id)}`} className="btn btn-secondary">
            Reply
          </Link>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void handleViewRaw()}
            disabled={rawLoading}
          >
            View MIME
          </button>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void handleDownloadRaw()}
            disabled={rawLoading}
          >
            Download
          </button>
          <button className="btn btn-danger" onClick={() => setConfirmDelete(true)}>
            Delete
          </button>
          <Link to="/" className="btn btn-secondary">
            Back
          </Link>
        </div>
      </header>
      <div className="email-body">
        {email.html ? (
          <iframe
            className="email-html-frame"
            title="Email content"
            sandbox=""
            srcDoc={emailHtmlDocument(email.html)}
          />
        ) : (
          <pre>{email.body}</pre>
        )}
      </div>
      <ConfirmDialog
        open={confirmDelete}
        title="Delete email"
        message="Delete this email? This cannot be undone."
        busy={deleting}
        onConfirm={handleDelete}
        onCancel={() => setConfirmDelete(false)}
      />
      {showRaw && (
        <div
          className="modal-overlay"
          role="presentation"
          onClick={(event) => {
            if (event.target === event.currentTarget) setShowRaw(false);
          }}
        >
          <div className="modal-card modal-wide" role="dialog" aria-modal="true" aria-label="Raw email MIME">
            <div className="modal-header">
              <h3>Raw email MIME</h3>
            </div>
            <div className="modal-body">
              {rawLoading ? (
                <div className="spinner small"></div>
              ) : rawError ? (
                <p className="confirm-message">{rawError}</p>
              ) : (
                <pre className="raw-mime-content">{rawCache?.id === email.id ? rawCache.mime : ''}</pre>
              )}
            </div>
            <div className="modal-footer">
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => void handleDownloadRaw()}
                disabled={rawLoading}
              >
                Download
              </button>
              <button type="button" className="btn btn-secondary" onClick={() => setShowRaw(false)}>
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </article>
  );
}