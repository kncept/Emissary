import { useEffect, useMemo, useState } from 'react'
import { errorMessage, getEmail, listEmails, markUnread } from './api'
import BrandMark from './BrandMark'
import Compose from './Compose'
import type { Email, EmailAddress } from './types'

type Props = {
  onLogout: () => void
}

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  hour: 'numeric',
  minute: '2-digit',
})
const shortDateFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
})
const fullDateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
})

function parseDate(value: string): Date | null {
  if (!value) {
    return null
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 1970) {
    return null
  }
  return date
}

function listDate(value: string): string {
  const date = parseDate(value)
  if (!date) {
    return ''
  }
  const now = new Date()
  if (date.toDateString() === now.toDateString()) {
    return timeFormatter.format(date)
  }
  if (date.getFullYear() === now.getFullYear()) {
    return shortDateFormatter.format(date)
  }
  return date.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

function fullDate(value: string): string {
  const date = parseDate(value)
  return date ? fullDateFormatter.format(date) : 'Unknown date'
}

function addressLabel(address?: EmailAddress): string {
  if (!address) {
    return 'Unknown sender'
  }
  return address.name?.trim() || address.address || 'Unknown sender'
}

function addressList(addresses?: EmailAddress[]): string {
  if (!addresses || addresses.length === 0) {
    return 'undisclosed recipients'
  }
  return addresses.map(addressLabel).join(', ')
}

function initials(address?: EmailAddress): string {
  const label = addressLabel(address)
  const words = label.split(/\s+/).filter(Boolean)
  if (words.length > 1) {
    return (words[0][0] + words[1][0]).toUpperCase()
  }
  return label.slice(0, 2).toUpperCase()
}

function preview(message: Email): string {
  const text = message.text || message.html?.replace(/<[^>]*>/g, ' ') || ''
  return text.replace(/\s+/g, ' ').trim().slice(0, 140)
}

export default function Mailbox({ onLogout }: Props) {
  const [emails, setEmails] = useState<Email[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selectedEmail, setSelectedEmail] = useState<Email | null>(null)
  const [loading, setLoading] = useState(true)
  const [messageLoading, setMessageLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [messageError, setMessageError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [refreshCounter, setRefreshCounter] = useState(0)
  const [composeDefaults, setComposeDefaults] = useState<{
    to: string
    subject: string
  } | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    listEmails()
      .then((messages) => {
        if (cancelled) {
          return
        }
        setEmails(messages)
        setSelectedId((current) => {
          if (current && messages.some((message) => message.id === current)) {
            return current
          }
          return messages[0]?.id ?? null
        })
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(errorMessage(err))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [refreshCounter])

  useEffect(() => {
    if (!selectedId) {
      setSelectedEmail(null)
      setMessageLoading(false)
      return
    }

    let cancelled = false
    setMessageLoading(true)
    setMessageError(null)

    getEmail(selectedId)
      .then((message) => {
        if (cancelled) {
          return
        }
        setSelectedEmail(message)
        setEmails((current) =>
          current.map((item) =>
            item.id === message.id ? { ...item, read: true } : item,
          ),
        )
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setMessageError(errorMessage(err))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setMessageLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [selectedId, refreshCounter])

  useEffect(() => {
    if (!notice) {
      return
    }
    const timer = window.setTimeout(() => setNotice(null), 3000)
    return () => window.clearTimeout(timer)
  }, [notice])

  const filteredEmails = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) {
      return emails
    }
    return emails.filter((message) => {
      const haystack = [
        message.subject,
        message.text,
        message.from.name ?? '',
        message.from.address,
        ...message.to.map((address) => `${address.name ?? ''} ${address.address}`),
      ]
        .join(' ')
        .toLowerCase()
      return haystack.includes(needle)
    })
  }, [emails, query])

  const unreadCount = emails.filter((message) => !message.read).length

  function refresh() {
    setRefreshCounter((value) => value + 1)
  }

  function selectEmail(id: string) {
    if (id === selectedId) {
      return
    }
    setSelectedEmail(null)
    setMessageError(null)
    setSelectedId(id)
  }

  function backToList() {
    setSelectedId(null)
    setSelectedEmail(null)
    setMessageError(null)
  }

  function openCompose() {
    setComposeDefaults({ to: '', subject: '' })
  }

  function openReply(message: Email) {
    const subject = message.subject
      ? message.subject.toLowerCase().startsWith('re:')
        ? message.subject
        : `Re: ${message.subject}`
      : 'Re:'
    setComposeDefaults({ to: message.from.address, subject })
  }

  function handleSent() {
    setComposeDefaults(null)
    setNotice('Message sent')
    refresh()
  }

  async function handleMarkUnread() {
    if (!selectedEmail) {
      return
    }
    try {
      await markUnread(selectedEmail.id)
      setEmails((current) =>
        current.map((message) =>
          message.id === selectedEmail.id ? { ...message, read: false } : message,
        ),
      )
      setSelectedEmail({ ...selectedEmail, read: false })
      setNotice('Marked as unread')
    } catch (err) {
      setMessageError(errorMessage(err))
    }
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand">
          <BrandMark />
          <span>Emissary</span>
        </div>
        <div className="topbar-actions">
          <button className="btn" type="button" onClick={refresh} disabled={loading}>
            Refresh
          </button>
          <button className="btn primary" type="button" onClick={openCompose}>
            Compose
          </button>
          <button className="btn ghost" type="button" onClick={onLogout}>
            Sign out
          </button>
        </div>
      </header>

      <div className={`workspace ${selectedId ? 'has-selection' : ''}`}>
        <aside className="mail-list-pane">
          <div className="list-toolbar">
            <div className="list-heading">
              <h2>Inbox</h2>
              <span className="badge">{emails.length}</span>
            </div>
            <input
              className="search"
              type="search"
              placeholder="Search mail"
              aria-label="Search mail"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            <p className="list-summary">
              {unreadCount} unread · {emails.length} total
            </p>
          </div>

          {loading ? (
            <div className="loading-state">
              <span className="spinner" aria-hidden="true" />
              Loading mail…
            </div>
          ) : null}

          {!loading && error ? (
            <div className="state-block error-state">
              <p>{error}</p>
              <button className="btn" type="button" onClick={refresh}>
                Try again
              </button>
            </div>
          ) : null}

          {!loading && !error && filteredEmails.length === 0 ? (
            <div className="state-block">
              <p>{emails.length === 0 ? 'Your inbox is empty.' : 'No messages match your search.'}</p>
            </div>
          ) : null}

          {!loading && !error && filteredEmails.length > 0 ? (
            <div className="mail-list">
              {filteredEmails.map((message) => (
                <button
                  key={message.id}
                  type="button"
                  className={`mail-row ${selectedId === message.id ? 'selected' : ''} ${
                    message.read ? '' : 'unread'
                  }`}
                  aria-pressed={selectedId === message.id}
                  onClick={() => selectEmail(message.id)}
                >
                  <span className="mail-row-top">
                    <span className="mail-sender">{addressLabel(message.from)}</span>
                    <time dateTime={message.date}>{listDate(message.date)}</time>
                  </span>
                  <span className="mail-subject">
                    {message.subject || '(no subject)'}
                  </span>
                  <span className="mail-preview">{preview(message)}</span>
                </button>
              ))}
            </div>
          ) : null}
        </aside>

        <section className="reading-pane">
          {!selectedId ? (
            <div className="reading-empty">
              <BrandMark />
              <h2>Select a message</h2>
              <p>Choose a conversation from the inbox to read it here.</p>
            </div>
          ) : null}

          {selectedId && messageLoading ? (
            <div className="loading-state reading-loading">
              <span className="spinner" aria-hidden="true" />
              Opening message…
            </div>
          ) : null}

          {selectedId && !messageLoading && messageError ? (
            <div className="state-block error-state reading-error">
              <p>{messageError}</p>
              <button className="btn" type="button" onClick={refresh}>
                Reload inbox
              </button>
            </div>
          ) : null}

          {selectedId && !messageLoading && !messageError && selectedEmail ? (
            <article className="message-card">
              <header className="message-header">
                <div className="message-toolbar">
                  <button className="btn ghost mobile-back" type="button" onClick={backToList}>
                    ← Inbox
                  </button>
                  <div className="message-actions">
                    <button
                      className="btn"
                      type="button"
                      onClick={() => openReply(selectedEmail)}
                    >
                      Reply
                    </button>
                    <button
                      className="btn"
                      type="button"
                      onClick={handleMarkUnread}
                      disabled={!selectedEmail.read}
                    >
                      Mark unread
                    </button>
                  </div>
                </div>

                <h1 className="message-subject">
                  {selectedEmail.subject || '(no subject)'}
                </h1>

                <div className="message-meta">
                  <span className="avatar" aria-hidden="true">
                    {initials(selectedEmail.from)}
                  </span>
                  <div className="message-people">
                    <p>
                      <strong>{addressLabel(selectedEmail.from)}</strong>
                      {selectedEmail.from.name ? (
                        <span className="muted"> &lt;{selectedEmail.from.address}&gt;</span>
                      ) : null}
                    </p>
                    <p className="muted">to {addressList(selectedEmail.to)}</p>
                    {selectedEmail.cc && selectedEmail.cc.length > 0 ? (
                      <p className="muted">cc {addressList(selectedEmail.cc)}</p>
                    ) : null}
                  </div>
                  <time className="message-date" dateTime={selectedEmail.date}>
                    {fullDate(selectedEmail.date)}
                  </time>
                </div>
              </header>

              <div className="message-body">
                {selectedEmail.html ? (
                  <iframe
                    className="message-frame"
                    title="Email content"
                    sandbox=""
                    referrerPolicy="no-referrer"
                    srcDoc={selectedEmail.html}
                  />
                ) : (
                  <pre className="message-text">
                    {selectedEmail.text || 'This message has no text content.'}
                  </pre>
                )}
              </div>
            </article>
          ) : null}
        </section>
      </div>

      {composeDefaults ? (
        <Compose
          initialTo={composeDefaults.to}
          initialSubject={composeDefaults.subject}
          onClose={() => setComposeDefaults(null)}
          onSent={handleSent}
        />
      ) : null}

      {notice ? (
        <div className="toast" role="status">
          {notice}
        </div>
      ) : null}
    </div>
  )
}
