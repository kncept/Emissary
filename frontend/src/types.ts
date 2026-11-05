export type EmailAddress = {
  name?: string
  address: string
}

export type Email = {
  id: string
  from: EmailAddress
  to: EmailAddress[]
  cc?: EmailAddress[]
  bcc?: EmailAddress[]
  subject: string
  date: string
  text: string
  html?: string
  read: boolean
  size?: number
}

export type LoginResponse = {
  token: string
  tokenType: string
  expiresAt: string
}

export type EmailListResponse = {
  emails: Email[]
}

export type SendEmailRequest = {
  to: string[]
  cc?: string[]
  bcc?: string[]
  subject: string
  text: string
  html?: string
}
