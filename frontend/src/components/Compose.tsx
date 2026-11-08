import { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { getEmail, sendEmail } from '../api';

interface ComposeProps {
  admin?: boolean;
  currentUser?: string;
}

export default function Compose({ admin = false, currentUser }: ComposeProps) {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [from, setFrom] = useState(admin ? '' : currentUser || '');
  const [to, setTo] = useState('');
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);

  useEffect(() => {
    if (!id) {
      setFrom(admin ? '' : currentUser || '');
      return;
    }

    let active = true;
    getEmail(id)
      .then((original) => {
        if (!active) return;
        setTo(original.from);
        setSubject(original.subject ? `Re: ${original.subject}` : 'Re:');
        if (admin) {
          // Reply as the user whose inbox the original message is in.
          setFrom(original.to);
        } else {
          setFrom(currentUser || '');
        }
      })
      .catch(() => {
        if (active) setError('Failed to load the email being replied to');
      });

    return () => {
      active = false;
    };
  }, [id, admin, currentUser]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSending(true);

    const result = await sendEmail({
      from: from || undefined,
      to,
      subject,
      body,
      replyToId: id,
    });
    if (result.success) {
      setSuccess(true);
      setTimeout(() => navigate('/'), 1500);
    } else {
      setError(result.message || 'Failed to send email');
    }
    setSending(false);
  };

  return (
    <div className="compose-container">
      <div className="compose-card">
        <header className="compose-header">
          <h2>{id ? 'Reply' : 'Compose'}</h2>
          <button className="btn btn-secondary" onClick={() => navigate('/')}>
            Cancel
          </button>
        </header>
        {error && <div className="error-message">{error}</div>}
        {success && <div className="success-message">Email sent successfully!</div>}
        <form onSubmit={handleSubmit} className="compose-form">
          {admin && (
            <div className="form-group">
              <label htmlFor="from">From</label>
              <input
                id="from"
                type="email"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                placeholder="Send as any user"
                disabled={sending}
              />
            </div>
          )}
          <div className="form-group">
            <label htmlFor="to">To</label>
            <input
              id="to"
              type="email"
              value={to}
              onChange={(e) => setTo(e.target.value)}
              required
              placeholder="recipient@example.com"
              disabled={sending}
            />
          </div>
          <div className="form-group">
            <label htmlFor="subject">Subject</label>
            <input
              id="subject"
              type="text"
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              required
              placeholder="Subject"
              disabled={sending}
            />
          </div>
          <div className="form-group">
            <label htmlFor="body">Body</label>
            <textarea
              id="body"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              required
              placeholder="Write your email here..."
              rows={15}
              disabled={sending}
            />
          </div>
          <div className="compose-actions">
            <button type="submit" className="btn btn-primary" disabled={sending}>
              {sending ? 'Sending...' : 'Send'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
