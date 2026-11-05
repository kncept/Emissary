import { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { sendEmail } from '../api';

export default function Compose() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [to, setTo] = useState('');
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);

  useEffect(() => {
    // If replying to an email, we could load the original email here
    // For now, just handle the case where we might pre-fill from a reply
  }, [id]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSending(true);

    const result = await sendEmail({ to, subject, body });
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