import type {
  Email,
  EmailListResponse,
  LoginResponse,
  SendEmailRequest,
} from './types'

const TOKEN_KEY = 'emissary.token'

export class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string | null): void {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token)
  } else {
    localStorage.removeItem(TOKEN_KEY)
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const token = getToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }

  const response = await fetch(`/api${path}`, { ...init, headers })

  if (response.status === 401) {
    setToken(null)
    window.dispatchEvent(new Event('emissary:unauthorized'))
  }

  if (!response.ok) {
    let message = `Request failed (${response.status})`
    try {
      const body = (await response.json()) as { error?: string }
      if (body.error) {
        message = body.error
      }
    } catch {
      // The response did not contain JSON.
    }
    throw new ApiError(message, response.status)
  }

  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

export async function login(
  username: string,
  password: string,
): Promise<LoginResponse> {
  const response = await request<LoginResponse>('/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  })
  setToken(response.token)
  return response
}

export async function listEmails(): Promise<Email[]> {
  const response = await request<EmailListResponse>('/emails')
  return response.emails
}

export async function getEmail(id: string): Promise<Email> {
  return request<Email>(`/emails/${encodeURIComponent(id)}`)
}

export async function markUnread(id: string): Promise<void> {
  await request<{ ok: boolean }>(
    `/emails/${encodeURIComponent(id)}/unread`,
    { method: 'POST' },
  )
}

export async function sendEmail(message: SendEmailRequest): Promise<void> {
  await request<{ ok: boolean }>('/send', {
    method: 'POST',
    body: JSON.stringify(message),
  })
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.message
  }
  if (error instanceof Error) {
    return error.message
  }
  return 'Something went wrong'
}
