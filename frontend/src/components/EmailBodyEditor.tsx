import { useRef, useState, type MouseEvent } from 'react';
import Editor, {
  BtnBold,
  BtnBulletList,
  BtnItalic,
  BtnNumberedList,
  BtnUnderline,
  Separator,
  Toolbar,
} from 'react-simple-wysiwyg';
import type { EmailBodyFormat } from '../types.ts';

interface EmailBodyEditorProps {
  format: EmailBodyFormat;
  onFormatChange: (format: EmailBodyFormat) => void;
  plainBody: string;
  onPlainBodyChange: (value: string) => void;
  htmlBody: string;
  onHtmlBodyChange: (value: string) => void;
  placeholder?: string;
  rows?: number;
  disabled?: boolean;
  ariaLabel?: string;
  textareaId?: string;
}

// The library's link button uses window.prompt. Use an in-app field instead so
// the editor never opens a browser dialog.
function selectedAnchor(): HTMLAnchorElement | null {
  let node = window.getSelection()?.anchorNode ?? null;
  while (node && node !== document.body) {
    if (node instanceof HTMLAnchorElement) return node;
    node = node.parentNode;
  }
  return null;
}

/**
 * The shared plain-text/HTML body editor used by Compose and VacationResponse.
 * It owns only the editor UI; callers keep the body values and decide how to
 * persist them.
 */
export default function EmailBodyEditor({
  format,
  onFormatChange,
  plainBody,
  onPlainBodyChange,
  htmlBody,
  onHtmlBodyChange,
  placeholder = 'Write your email here...',
  rows = 15,
  disabled = false,
  ariaLabel = 'Body',
  textareaId = 'body',
}: EmailBodyEditorProps) {
  const [linkOpen, setLinkOpen] = useState(false);
  const [linkUrl, setLinkUrl] = useState('https://');
  const [linkError, setLinkError] = useState('');
  const savedRange = useRef<Range | null>(null);

  const handleLinkClick = (event: MouseEvent<HTMLButtonElement>) => {
    event.preventDefault();
    if (disabled) return;
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

  return (
    <div className="compose-body">
      <div className="compose-body-toolbar">
        <div className="format-toggle" role="group" aria-label="Body format">
          <button
            type="button"
            className={format === 'plain' ? 'active' : ''}
            aria-pressed={format === 'plain'}
            onClick={() => onFormatChange('plain')}
            disabled={disabled}
          >
            Plain text
          </button>
          <button
            type="button"
            className={format === 'html' ? 'active' : ''}
            aria-pressed={format === 'html'}
            onClick={() => onFormatChange('html')}
            disabled={disabled}
          >
            HTML
          </button>
        </div>
      </div>
      {format === 'plain' ? (
        <textarea
          id={textareaId}
          aria-label={ariaLabel}
          value={plainBody}
          onChange={(event) => onPlainBodyChange(event.target.value)}
          placeholder={placeholder}
          rows={rows}
          disabled={disabled}
        />
      ) : (
        <>
          <Editor
            value={htmlBody}
            onChange={(event) => onHtmlBodyChange(event.target.value)}
            placeholder={placeholder}
            aria-label={ariaLabel}
            disabled={disabled}
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
                disabled={disabled}
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
                onChange={(event) => setLinkUrl(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault();
                    applyLink();
                  } else if (event.key === 'Escape') {
                    event.stopPropagation();
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
  );
}
