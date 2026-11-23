import type { MailboxRole } from './types.ts';

export interface MailboxDefinition {
  role: MailboxRole;
  label: string;
  path: string;
  icon: string;
  emptyMessage: string;
  emptyAction?: string;
}

// Shared by the left mailbox panel, the list header, and the message view so
// folder labels and back links stay in sync across routes.
export const MAILBOXES: MailboxDefinition[] = [
  {
    role: 'inbox',
    label: 'Inbox',
    path: '/',
    icon: '📥',
    emptyMessage: 'No emails yet',
    emptyAction: 'Send your first email',
  },
  {
    role: 'drafts',
    label: 'Drafts',
    path: '/drafts',
    icon: '📝',
    emptyMessage: 'No drafts yet',
    emptyAction: 'Write a draft',
  },
  {
    role: 'sent',
    label: 'Sent',
    path: '/sent',
    icon: '📤',
    emptyMessage: 'No sent messages yet',
    emptyAction: 'Send an email',
  },
  {
    role: 'archive',
    label: 'Archive',
    path: '/archive',
    icon: '🗄️',
    emptyMessage: 'Nothing archived',
  },
  {
    role: 'junk',
    label: 'Junk',
    path: '/junk',
    icon: '🚫',
    emptyMessage: 'No junk mail',
  },
  {
    role: 'trash',
    label: 'Trash',
    path: '/trash',
    icon: '🗑️',
    emptyMessage: 'Trash is empty',
  },
];

const BY_ROLE = new Map(MAILBOXES.map((mailbox) => [mailbox.role, mailbox]));

export function mailboxDefinition(role: MailboxRole): MailboxDefinition {
  return BY_ROLE.get(role) ?? MAILBOXES[0];
}

export function mailboxPath(role: MailboxRole): string {
  return mailboxDefinition(role).path;
}

export function mailboxLabel(role: MailboxRole): string {
  return mailboxDefinition(role).label;
}

export function isMailboxRole(value: unknown): value is MailboxRole {
  return typeof value === 'string' && BY_ROLE.has(value as MailboxRole);
}
