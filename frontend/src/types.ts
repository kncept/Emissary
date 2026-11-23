/** JMAP role mailboxes exposed by the backend's default account setup. */
export type MailboxRole = 'inbox' | 'drafts' | 'sent' | 'archive' | 'junk' | 'trash';

export interface Email {
  id: string;
  from: string;
  to: string;
  subject: string;
  body: string;
  html?: string;
  timestamp: string;
  read: boolean;
  flagged: boolean;
  /** RFC 5322 Message-ID of the message (with angle brackets). */
  messageId?: string;
  /** Original References header values, oldest first. */
  references?: string[];
  /** Reply-To addresses; replies go here when present, otherwise to From. */
  replyTo?: string[];
}

export interface AuthState {
  authenticated: boolean;
  username?: string;
  email?: string;
  systemManager?: boolean;
  emissaryUserManagement?: boolean;
}

export interface LoginResponse {
  token?: string;
  tokenType?: string;
  expiresAt?: string;
  username?: string;
  email?: string;
  systemManager?: boolean;
  emissaryUserManagement?: boolean;
  error?: string;
}

export type EmailBodyFormat = 'plain' | 'html';

export interface SendEmailRequest {
  /** Comma-separated To recipients. The server locks From to the
   * authenticated account, so no From is sent. */
  to: string;
  /** Comma-separated Cc recipients. */
  cc?: string;
  /** Comma-separated Bcc recipients. */
  bcc?: string;
  subject: string;
  body: string;
  format: EmailBodyFormat;
  /** Message-IDs carried as the In-Reply-To header (usually one). */
  inReplyTo?: string[];
  /** Message-IDs carried as the References header, oldest first. */
  references?: string[];
}