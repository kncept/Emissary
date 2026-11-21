import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type MouseEvent as ReactMouseEvent,
} from 'react';
import { getVacationResponse, setVacationResponse } from '../api.ts';
import type { VacationResponse, VacationResponseUpdate } from '../api/generated/schemas/jmap';
import { isHtmlEmpty } from '../html.ts';
import type { EmailBodyFormat } from '../types.ts';
import EmailBodyEditor from './EmailBodyEditor.tsx';

interface VacationSettingsProps {
  onClose: () => void;
}

interface VacationForm {
  isEnabled: boolean;
  fromDate: string;
  toDate: string;
  subject: string;
}

interface LoadedSettings {
  form: VacationForm;
  format: EmailBodyFormat;
  plainBody: string;
  htmlBody: string;
}

const EMPTY_FORM: VacationForm = {
  isEnabled: false,
  fromDate: '',
  toDate: '',
  subject: '',
};

/** Converts a JMAP UTCDate into the local value used by datetime-local. */
function toDateTimeLocal(value: string | null | undefined): string {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (part: number) => String(part).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** Converts a datetime-local value back into a JMAP UTCDate (RFC 3339 / Z). */
function toUtcDate(value: string): string | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

/** Empty and whitespace-only values clear the nullable JMAP property. */
function nullableText(value: string): string | null {
  return value.trim() ? value : null;
}

function settingsFromResponse(response: VacationResponse): LoadedSettings {
  const plainBody = response.textBody ?? '';
  const htmlBody = response.htmlBody ?? '';
  return {
    form: {
      isEnabled: response.isEnabled ?? false,
      fromDate: toDateTimeLocal(response.fromDate),
      toDate: toDateTimeLocal(response.toDate),
      subject: response.subject ?? '',
    },
    // Prefer the editor that has real content; otherwise start with plain text.
    format: !isHtmlEmpty(htmlBody) && !plainBody ? 'html' : 'plain',
    plainBody,
    htmlBody,
  };
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

export default function VacationSettings({ onClose }: VacationSettingsProps) {
  const [form, setForm] = useState<VacationForm>(EMPTY_FORM);
  const [format, setFormat] = useState<EmailBodyFormat>('plain');
  const [plainBody, setPlainBody] = useState('');
  const [htmlBody, setHtmlBody] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const closeButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    closeButton.current?.focus();
    return () => previousFocus?.focus();
  }, []);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !saving) {
        event.preventDefault();
        onClose();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose, saving]);

  useEffect(() => {
    let active = true;
    getVacationResponse()
      .then((response) => {
        if (!active) return;
        const loaded = settingsFromResponse(response);
        setForm(loaded.form);
        setFormat(loaded.format);
        setPlainBody(loaded.plainBody);
        setHtmlBody(loaded.htmlBody);
      })
      .catch((err) => {
        if (active) setLoadError(errorMessage(err, 'Failed to load vacation response.'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [reloadKey]);

  const updateField = (field: keyof VacationForm, value: string | boolean) => {
    setForm((current) => ({ ...current, [field]: value }));
    setSuccess(null);
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError(null);
    setSuccess(null);

    const fromDate = toUtcDate(form.fromDate);
    const toDate = toUtcDate(form.toDate);
    if (form.fromDate && !fromDate) {
      setError('From date is not a valid date and time.');
      return;
    }
    if (form.toDate && !toDate) {
      setError('To date is not a valid date and time.');
      return;
    }
    if (fromDate && toDate && fromDate >= toDate) {
      setError('From date must be before to date.');
      return;
    }

    const update: VacationResponseUpdate = {
      isEnabled: form.isEnabled,
      fromDate,
      toDate,
      subject: form.subject.trim() || null,
      textBody: nullableText(plainBody),
      htmlBody: isHtmlEmpty(htmlBody) ? null : htmlBody,
    };

    setSaving(true);
    try {
      const saved = await setVacationResponse(update);
      const loaded = settingsFromResponse(saved);
      setForm(loaded.form);
      setFormat(loaded.format);
      setPlainBody(loaded.plainBody);
      setHtmlBody(loaded.htmlBody);
      setSuccess('Vacation response saved.');
    } catch (err) {
      setError(errorMessage(err, 'Failed to save vacation response.'));
    } finally {
      setSaving(false);
    }
  };

  const reload = () => {
    setError(null);
    setSuccess(null);
    setLoadError(null);
    setLoading(true);
    setReloadKey((key) => key + 1);
  };

  const handleOverlayClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget && !saving) onClose();
  };

  return (
    <div className="modal-overlay vacation-modal-overlay" onMouseDown={handleOverlayClick}>
      <div
        className="modal-card vacation-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="vacation-title"
      >
        <div className="modal-header">
          <h3 id="vacation-title">Vacation Response</h3>
          <button
            ref={closeButton}
            type="button"
            className="icon-btn"
            onClick={onClose}
            aria-label="Close vacation response"
            disabled={saving}
          >
            ✕
          </button>
        </div>
        <div className="modal-body">
          <p className="vacation-hint">
            Send an automatic reply when mail is delivered to this account. Replies are
            only sent while enabled and within the optional date range.
          </p>

          {loading ? (
            <p className="vacation-hint">Loading vacation settings…</p>
          ) : loadError ? (
            <>
              <div className="error-message">{loadError}</div>
              <button type="button" className="btn btn-secondary" onClick={reload} style={{ marginTop: '0.75rem' }}>
                Retry
              </button>
            </>
          ) : (
            <form id="vacation-form" className="compose-form" onSubmit={handleSubmit}>
              <div className="form-group">
                <label id="vacation-enabled-label">Automatic reply</label>
                <div
                  className="format-toggle"
                  role="group"
                  aria-labelledby="vacation-enabled-label"
                >
                  <button
                    type="button"
                    className={!form.isEnabled ? 'active' : ''}
                    aria-pressed={!form.isEnabled}
                    onClick={() => updateField('isEnabled', false)}
                    disabled={saving}
                  >
                    Disabled
                  </button>
                  <button
                    type="button"
                    className={form.isEnabled ? 'active' : ''}
                    aria-pressed={form.isEnabled}
                    onClick={() => updateField('isEnabled', true)}
                    disabled={saving}
                  >
                    Enabled
                  </button>
                </div>
              </div>

              <div className="form-group">
                <label htmlFor="vacation-from">From date (optional)</label>
                <input
                  id="vacation-from"
                  type="datetime-local"
                  value={form.fromDate}
                  onChange={(event) => updateField('fromDate', event.target.value)}
                  disabled={saving}
                />
              </div>

              <div className="form-group">
                <label htmlFor="vacation-to">To date (optional)</label>
                <input
                  id="vacation-to"
                  type="datetime-local"
                  value={form.toDate}
                  onChange={(event) => updateField('toDate', event.target.value)}
                  disabled={saving}
                />
              </div>

              <div className="form-group">
                <label htmlFor="vacation-subject">Subject (optional)</label>
                <input
                  id="vacation-subject"
                  type="text"
                  value={form.subject}
                  onChange={(event) => updateField('subject', event.target.value)}
                  disabled={saving}
                />
              </div>

              <EmailBodyEditor
                format={format}
                onFormatChange={(next) => {
                  setFormat(next);
                  setSuccess(null);
                }}
                plainBody={plainBody}
                onPlainBodyChange={(value) => {
                  setPlainBody(value);
                  setSuccess(null);
                }}
                htmlBody={htmlBody}
                onHtmlBodyChange={(value) => {
                  setHtmlBody(value);
                  setSuccess(null);
                }}
                placeholder="Write your automatic reply here..."
                rows={10}
                disabled={saving}
                ariaLabel="Vacation reply body"
                textareaId="vacation-body"
              />
            </form>
          )}

          {error && <div className="error-message">{error}</div>}
          {success && <div className="success-message">{success}</div>}
        </div>
        {!loading && !loadError && (
          <div className="modal-footer">
            <button type="button" className="btn btn-secondary" onClick={onClose} disabled={saving}>
              Cancel
            </button>
            <button type="button" className="btn btn-secondary" onClick={reload} disabled={saving}>
              Reload
            </button>
            <button type="submit" form="vacation-form" className="btn btn-primary" disabled={saving}>
              {saving ? 'Saving…' : 'Save vacation response'}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
