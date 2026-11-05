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
}

export interface LoginResponse {
  token?: string;
  tokenType?: string;
  expiresAt?: string;
  error?: string;
}

export interface SendEmailRequest {
  to: string;
  subject: string;
  body: string;
}