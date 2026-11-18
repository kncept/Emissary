import { useEffect, useRef, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import Editor, {
  BtnBold,
  BtnBulletList,
  BtnItalic,
  BtnNumberedList,
  BtnUnderline,
  Separator,
  Toolbar,
} from 'react-simple-wysiwyg';
import { getEmail, sendEmail } from '../api.ts';
import { isHtmlEmpty } from '../html.ts';
import type { Email, EmailBodyFormat } from '../types.ts';

interface ComposeProps {
  systemManager?: boolean;
  currentUser?: string;
}

// The library's link button uses window.prompt. Use an in-app field instead so
// the compose flow never opens a browser dialog.
function selectedAnchor(): HTMLAnchorElement | null {
  let node = window.getSelection()?.anchorNode ?? null;
  while (node && node !== document.body) {
    if (node instanceof HTMLAnchorElement) return node;
    node = node.parentNode;
  }
  return null;
}

export default function Compose({ systemManager = false, currentUser }: ComposeProps) {
  const { id } = useParams<{ id: string }>();
  // Reply links encode the full delivered object key; decode only when a
  // router left the escaped form intact.
  const emailID = (() => {
    if (!id || !/%2f/i.test(id)) return id;
    try {
      return decodeURIComponent(id);
    } catch {
      return id;
    }
  })();
  const navigate = useNavigate();
  const [from, setFrom] = useState(systemManager ? '' : currentUser || '');
  const [to, setTo] = useState('');
  const [subject, setSubject] = useState('');
  const [format, setFormat] = useState<EmailBodyFormat>('html');
  const [plainBody, setPlainBody] = useState('');
  const [htmlBody, setHtmlBody] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const [linkUrl, setLinkUrl] = useState('https://');
  const [linkError, setLinkError] = useState('');
  const savedRange = useRef<Range | null>(null);

  useEffect(() => {
    if (!emailID) {
      setFrom(systemManager ? '' : currentUser || '');
      return;
    }

    let active = true;
    getEmail(emailID)
      .then((original: Email) => {
        if (!active) return;
        setTo(original.from);
        setSubject(original.subject ? `Re: ${original.subject}` : 'Re:');
        if (systemManager) {
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
  }, [emailID, systemManager, currentUser]);

  const handleLinkClick = (e: React.MouseEvent<HTMLButtonElement>) => {
    e.preventDefault();
    if (sending) return;
    if (selectedAnchor()) {
      document.execCommand('unlink');
      return;
    }
    const selection = window.getSelection();
    savedRange.current = selection && selection.rangeCount > 0
      ? selection.getRangeAt(0).cloneRange()
      : null;
    setLinkUrl('https://');
    setLinkError('');
    setLinkOpen(true);
  };

  const applyLink = () => {
    const url = linkUrl.trim();
    if (!/^https?:\/\/\S+$/i.test(url)) {
      setLinkError('Enter a full URL starting with https://');
      return;
    }
    const selection = window.getSelection();
    if (savedRange.current && selection) {
      selection.removeAllRanges();
      selection.addRange(savedRange.current);
    }
    document.execCommand('createLink', false, url);
    setLinkOpen(false);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    const body = format === 'plain' ? plainBody : htmlBody;
    const empty = format === 'plain' ? !body.trim() : isHtmlEmpty(body);
    if (empty) {
      setError('Please enter a message body');
      return;
    }
    setSending(true);

    const result = await sendEmail({
      from: from || undefined,
      to,
      subject,
      body,
      format,
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
          {systemManager && (
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
            <div className="body-heading">
              <label id="body-label">Body</label>
              <div className="format-toggle" role="group" aria-label="Body format">
                <button
                  type="button"
                  className={format === 'plain' ? 'active' : ''}
                  aria-pressed={format === 'plain'}
                  onClick={() => setFormat('plain')}
                  disabled={sending}
                >
                  Plain text
                </button>
                <button
                  type="button"
                  className={format === 'html' ? 'active' : ''}
                  aria-pressed={format === 'html'}
                  onClick={() => setFormat('html')}
                  disabled={sending}
                >
                  HTML
                </button>
              </div>
            </div>
            {format === 'plain' ? (
              <textarea
                id="body"
                value={plainBody}
                onChange={(e) => setPlainBody(e.target.value)}
                placeholder="Write your email here..."
                rows={15}
                disabled={sending}
              />
            ) : (
              <>
                <Editor
                  value={htmlBody}
                  onChange={(e) => setHtmlBody(e.target.value)}
                  placeholder="Write your email here..."
                  aria-labelledby="body-label"
                  disabled={sending}
                >
                  <Toolbar>
                    <BtnBold />
                    <BtnItalic />
                    <BtnUnderline />
                    <Separator />
                    <BtnBulletList />
                    <BtnNumberedList />
                    <Separator />
                    <button
                      type="button"
                      className="rsw-btn"
                      title="Add or remove link"
                      onMouseDown={handleLinkClick}
                      disabled={sending}
                    >
                      🔗
                    </button>
                  </Toolbar>
                </Editor>
                {linkOpen && (
                  <div className="link-form">
                    <input
                      type="url"
                      aria-label="Link URL"
                      value={linkUrl}
                      onChange={(e) => setLinkUrl(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          applyLink();
                        } else if (e.key === 'Escape') {
                          setLinkOpen(false);
                        }
                      }}
                      autoFocus
                      placeholder="https://example.com"
                    />
                    <button type="button" className="btn btn-primary" onClick={applyLink}>
                      Add link
                    </button>
                    <button type="button" className="btn btn-secondary" onClick={() => setLinkOpen(false)}>
                      Cancel
                    </button>
                    {linkError && <span className="link-error">{linkError}</span>}
                  </div>
                )}
              </>
            )}
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
