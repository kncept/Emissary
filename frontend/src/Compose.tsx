import { useEffect, useState, type FormEvent } from 'react'
import { errorMessage, sendEmail } from './api'

type Props = {
  initialTo?: string
  initialSubject?: string
  onClose: () => void
  onSent: () => void
}

function splitAddresses(value: string): string[] {
  return value
    .split(',')
    .map((address) => address.trim())
    .filter(Boolean)
}

export default function Compose({
  initialTo = '',
  initialSubject = '',
  onClose,
  onSent,
}: Props) {
  const [to, setTo] = useState(initialTo)
  const [cc, setCc] = useState('')
  const [bcc, setBcc] = useState('')
  const [subject, setSubject] = useState(initialSubject)
  const [text, setText] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)

  useEffect(() => {
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        onClose()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(null)

    const recipients = splitAddresses(to)
    if (recipients.length === 0) {
      setError('Add at least one recipient.')
      return
    }

    setSending(true)
    try {
      await sendEmail({
        to: recipients,
        cc: splitAddresses(cc),
        bcc: splitAddresses(bcc),
        subject,
        text,
        html: '',
      })
      onSent()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <div
      className="modal-backdrop"
      data-testid="modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose()
        }
      }}
    >
      <section className="compose" role="dialog" aria-modal="true" aria-label="Compose email">
        <header className="compose-header">
          <div>
            <p className="eyebrow">New message</p>
            <h2>Compose</h2>
          </div>
          <button className="btn ghost" type="button" onClick={onClose}>
            Close
          </button>
        </header>

        <form className="compose-form" onSubmit={handleSubmit} role="form">
          <div className="field">
            <label htmlFor="compose-to">To</label>
            <input
              id="compose-to"
              type="text"
              placeholder="person@example.com, another@example.com"
              autoFocus
              required
              value={to}
              onChange={(event) => setTo(event.target.value)}
            />
          </div>

          <div className="field-row">
            <div className="field">
              <label htmlFor="compose-cc">Cc</label>
              <input
                id="compose-cc"
                type="text"
                placeholder="optional"
                value={cc}
                onChange={(event) => setCc(event.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="compose-bcc">Bcc</label>
              <input
                id="compose-bcc"
                type="text"
                placeholder="optional"
                value={bcc}
                onChange={(event) => setBcc(event.target.value)}
              />
            </div>
          </div>

          <div className="field">
            <label htmlFor="compose-subject">Subject</label>
            <input
              id="compose-subject"
              type="text"
              value={subject}
              onChange={(event) => setSubject(event.target.value)}
            />
          </div>

          <div className="field">
            <label htmlFor="compose-body">Message</label>
            <textarea
              id="compose-body"
              value={text}
              onChange={(event) => setText(event.target.value)}
            />
          </div>

          {error ? (
            <p className="form-error" role="alert">
              {error}
            </p>
          ) : null}

          <div className="compose-actions">
            <button className="btn" type="button" onClick={onClose} disabled={sending}>
              Cancel
            </button>
            <button className="btn primary" type="submit" disabled={sending}>
              {sending ? 'Sending…' : 'Send message'}
            </button>
          </div>
        </form>
      </section>
    </div>
  )
}
