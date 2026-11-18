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
import { getEmail, sendEmail, MASQUERADE_AS_KEY, parseAddressList } from '../api.ts';
import { htmlToText, isHtmlEmpty } from '../html.ts';
import type { Email, EmailBodyFormat } from '../types.ts';

interface ComposeProps {
  /** 'reply' and 'forward' load the original email and prefill the draft. */
  mode?: 'reply' | 'forward';
  /** The authenticated user's address; the From field is locked to it (or to
   * the masquerade target when one is set). */
  currentUser?: string;
}

const PLAIN_SEPARATOR = '-----';

// A quoted original sits underneath the new text: exactly six blank lines
// above the separator line and one blank line below it (top posting).
function plainQuoteSuffix(quote: string): string {
  if (!quote) return '';
  return '\n'.repeat(7) + PLAIN_SEPARATOR + '\n'.repeat(2) + quote;
}

// The same layout in HTML: line breaks, a separator element and the quoted
// text in a preformatted block. htmlToText maps this back onto the exact
// plain-text layout above.
function htmlQuoteSuffix(quote: string): string {
  if (!quote) return '';
  const escaped = quote
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
  return (
    '<br>'.repeat(7) +
    `<div class="emissary-quote-separator">${PLAIN_SEPARATOR}</div>` +
    '<br>' +
    `<pre class="emissary-quote">${escaped}</pre>`
  );
}

function baseSubject(subject: string): string {
  const trimmed = subject.trim();
  return trimmed === '' || trimmed === '(No subject)' ? '' : trimmed;
}

// Prefixes the subject unless it already starts with the same prefix
// (case-insensitive), so a reply to a forwarded message reads "RE: FWD: ...".
function prefixedSubject(prefix: string, subject: string): string {
  const base = baseSubject(subject);
  if (!base) return prefix;
  if (new RegExp(`^${prefix.replace(/:/g, '')}:`, 'i').test(base)) return base;
  return `${prefix} ${base}`;
}

// Both replies and forwards set In-Reply-To to the original's Message-ID and
// References to the original's references with its Message-ID appended
// (duplicates removed, order preserved).
function threadHeaders(original: Email): { inReplyTo: string[]; references: string[] } {
  const messageId = (original.messageId || '').trim();
  if (!messageId) return { inReplyTo: [], references: [] };
  const references: string[] = [];
  for (const ref of [...(original.references || []), messageId]) {
    if (ref && !references.includes(ref)) references.push(ref);
  }
  return { inReplyTo: [messageId], references };
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

export default function Compose({ mode, currentUser }: ComposeProps) {
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
  // The From address is locked: the masquerade target when one is set,
  // otherwise the current user. The server enforces the same value, so this
  // field is display-only.
  const from = window.localStorage.getItem(MASQUERADE_AS_KEY) || currentUser || '';
  const [to, setTo] = useState('');
  const [cc, setCc] = useState('');
  const [bcc, setBcc] = useState('');
  const [showCcBcc, setShowCcBcc] = useState(false);
  const [subject, setSubject] = useState('');
  const [format, setFormat] = useState<EmailBodyFormat>('html');
  const [plainBody, setPlainBody] = useState('');
  const [htmlBody, setHtmlBody] = useState('');
  const [inReplyTo, setInReplyTo] = useState<string[]>([]);
  const [references, setReferences] = useState<string[]>([]);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const [linkUrl, setLinkUrl] = useState('https://');
  const [linkError, setLinkError] = useState('');
  const savedRange = useRef<Range | null>(null);

  useEffect(() => {
    if (!emailID) return;

    let active = true;
    getEmail(emailID)
      .then((original: Email) => {
        if (!active) return;
        const { inReplyTo: replyTo, references: refs } = threadHeaders(original);
        setInReplyTo(replyTo);
        setReferences(refs);
        // Quote the original's text body (an HTML-only original is quoted as
        // its extracted text) in both formats so switching preserves it.
        const quote = (original.body || htmlToText(original.html || '')).trim();
        setPlainBody(plainQuoteSuffix(quote));
        setHtmlBody(htmlQuoteSuffix(quote));
        if (mode === 'forward') {
          // A forward starts with no recipients; the user adds them.
          setSubject(prefixedSubject('FWD:', original.subject));
        } else {
          setTo(original.replyTo?.[0] || original.from || '');
          setSubject(prefixedSubject('RE:', original.subject));
        }
      })
      .catch(() => {
        if (active) setError('Failed to load the original email');
      });

    return () => {
      active = false;
    };
  }, [emailID, mode]);

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
    if (parseAddressList(to).length === 0) {
      setError('Please enter at least one recipient');
      return;
    }
    const body = format === 'plain' ? plainBody : htmlBody;
    const empty = format === 'plain' ? !body.trim() : isHtmlEmpty(body);
    if (empty) {
      setError('Please enter a message body');
      return;
    }
    setSending(true);

    try {
      const result = await sendEmail({
        to,
        cc,
        bcc,
        subject,
        body,
        format,
        inReplyTo,
        references,
      });
      if (result.success) {
        setSuccess(true);
        setTimeout(() => navigate('/'), 1500);
      } else {
        setError(result.message || 'Failed to send email');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to send email');
    }
    setSending(false);
  };

  const title = mode === 'reply' ? 'Reply' : mode === 'forward' ? 'Forward' : 'Compose';

  return (
    <div className="compose-container">
      <div className="compose-card">
        <header className="compose-header">
          <h2>{title}</h2>
          <button className="btn btn-secondary" onClick={() => navigate('/')}>
            Cancel
          </button>
        </header>
        {error && <div className="error-message">{error}</div>}
        {success && <div className="success-message">Email sent successfully!</div>}
        <form onSubmit={handleSubmit} className="compose-form">
          <div className="form-group">
            <label htmlFor="from">From</label>
            <input
              id="from"
              type="email"
              className="compose-from-locked"
              value={from}
              readOnly
              disabled={sending}
              title="Locked to your account"
            />
          </div>
          <div className="form-group">
            <label htmlFor="to">To</label>
            <input
              id="to"
              type="text"
              value={to}
              onChange={(e) => setTo(e.target.value)}
              placeholder="recipient@example.com, other@example.com"
              disabled={sending}
            />
          </div>
          {showCcBcc && (
            <>
              <div className="form-group">
                <label htmlFor="cc">Cc</label>
                <input
                  id="cc"
                  type="text"
                  value={cc}
                  onChange={(e) => setCc(e.target.value)}
                  placeholder="cc@example.com, other@example.com"
                  disabled={sending}
                />
              </div>
              <div className="form-group">
                <label htmlFor="bcc">Bcc</label>
                <input
                  id="bcc"
                  type="text"
                  value={bcc}
                  onChange={(e) => setBcc(e.target.value)}
                  placeholder="bcc@example.com"
                  disabled={sending}
                />
              </div>
            </>
          )}
          <div className="compose-ccbcc-toggle">
            <button
              type="button"
              className="ccbcc-toggle-btn"
              onClick={() => setShowCcBcc(!showCcBcc)}
              disabled={sending}
            >
              {showCcBcc ? 'Hide Cc/Bcc' : 'Add Cc/Bcc'}
            </button>
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
          <div className="compose-body">
            <div className="compose-body-toolbar">
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
                aria-label="Body"
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
                  aria-label="Body"
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
