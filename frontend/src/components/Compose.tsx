import { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { getEmail, sendEmail, MASQUERADE_AS_KEY, parseAddressList } from '../api.ts';
import { htmlToText, isHtmlEmpty } from '../html.ts';
import type { Email, EmailBodyFormat } from '../types.ts';
import EmailBodyEditor from './EmailBodyEditor.tsx';

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
          <EmailBodyEditor
            format={format}
            onFormatChange={setFormat}
            plainBody={plainBody}
            onPlainBodyChange={setPlainBody}
            htmlBody={htmlBody}
            onHtmlBodyChange={setHtmlBody}
            disabled={sending}
          />
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
