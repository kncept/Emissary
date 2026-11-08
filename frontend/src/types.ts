export interface Email {
  id: string;
  from: string;
  to: string;
  subject: string;
  body: string;
  timestamp: string;
  read: boolean;
  flagged: boolean;
}

export interface AuthState {
  authenticated: boolean;
  username?: string;
  email?: string;
  admin?: boolean;
}

export interface LoginResponse {
  token?: string;
  tokenType?: string;
  expiresAt?: string;
  username?: string;
  email?: string;
  admin?: boolean;
  error?: string;
}

export interface SendEmailRequest {
  from?: string;
  to: string;
  subject: string;
  body: string;
  replyToId?: string;
}