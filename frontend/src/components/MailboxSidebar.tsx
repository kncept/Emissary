import { Link, useLocation } from 'react-router-dom';
import { MAILBOXES, isMailboxRole } from '../mailboxes.ts';
import type { MailboxRole } from '../types.ts';
import { requestEmailRefresh } from '../api/email-checks.ts';

// The left-hand mailbox control panel: one link per JMAP role mailbox plus a
// compose shortcut. Folder links request a refresh so revisiting the folder
// you are already in also checks for new messages.
export default function MailboxSidebar() {
  const location = useLocation();

  const activeRole: MailboxRole | null = (() => {
    if (location.pathname.startsWith('/email/')) {
      const fromState = (location.state as { mailboxRole?: unknown } | null)?.mailboxRole;
      return isMailboxRole(fromState) ? fromState : 'inbox';
    }
    return MAILBOXES.find((mailbox) => mailbox.path === location.pathname)?.role ?? null;
  })();

  return (
    <nav className="mailbox-sidebar" aria-label="Mailboxes">
      <h2 className="mailbox-sidebar-title">Mailboxes</h2>
      <Link to="/compose" className="btn btn-primary mailbox-compose">
        <span className="nav-icon" aria-hidden="true">✏️</span>
        Compose
      </Link>
      <ul className="mailbox-list">
        {MAILBOXES.map((mailbox) => {
          const active = activeRole === mailbox.role;
          return (
            <li key={mailbox.role}>
              <Link
                to={mailbox.path}
                className={`mailbox-link${active ? ' active' : ''}`}
                aria-current={active ? 'page' : undefined}
                onClick={() => requestEmailRefresh(mailbox.role)}
              >
                <span className="nav-icon" aria-hidden="true">{mailbox.icon}</span>
                <span className="mailbox-label">{mailbox.label}</span>
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
