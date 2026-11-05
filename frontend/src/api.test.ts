import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  getToken,
  setToken,
  login,
  listEmails,
  getEmail,
  markUnread,
  sendEmail,
  errorMessage,
  ApiError,
} from './api'
import { mockFetch } from './test-setup'

const TOKEN_KEY = 'emissary.token'

describe('api', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('getToken / setToken', () => {
    it('returns null when no token stored', () => {
      expect(getToken()).toBeNull()
    })

    it('stores and retrieves token', () => {
      setToken('test-token')
      expect(localStorage.setItem).toHaveBeenCalledWith(TOKEN_KEY, 'test-token')
      expect(getToken()).toBe('test-token')
    })

    it('removes token when set to null', () => {
      setToken('test-token')
      setToken(null)
      expect(localStorage.removeItem).toHaveBeenCalledWith(TOKEN_KEY)
      expect(getToken()).toBeNull()
    })
  })

  describe('login', () => {
    it('posts credentials and stores token on success', async () => {
      const mockResponse = {
        token: 'jwt-token',
        tokenType: 'Bearer',
        expiresAt: new Date().toISOString(),
      }

      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockResponse),
      })

      const result = await login('user', 'pass')

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/login')
      expect(callArgs[1]).toMatchObject({
        method: 'POST',
        body: JSON.stringify({ username: 'user', password: 'pass' }),
      })
      expect(result).toEqual(mockResponse)
      expect(localStorage.setItem).toHaveBeenCalledWith(TOKEN_KEY, 'jwt-token')
    })

    it('throws ApiError on failure', async () => {
      mockFetch.mockResolvedValue({
        ok: false,
        status: 401,
        json: () => Promise.resolve({ error: 'Invalid credentials' }),
      })

      await expect(login('user', 'wrong')).rejects.toThrow(ApiError)
      await expect(login('user', 'wrong')).rejects.toMatchObject({
        message: 'Invalid credentials',
        status: 401,
      })
    })

    it('clears token on 401', async () => {
      setToken('existing-token')

      mockFetch.mockResolvedValue({
        ok: false,
        status: 401,
        json: () => Promise.resolve({ error: 'Unauthorized' }),
      })

      await expect(login('user', 'pass')).rejects.toThrow()
      expect(localStorage.removeItem).toHaveBeenCalledWith(TOKEN_KEY)
    })
  })

  describe('listEmails', () => {
    it('fetches emails with auth header', async () => {
      const mockEmails = [
        { id: '1', subject: 'Test', from: { address: 'a@b.com' }, to: [], date: '', text: '', read: false },
      ]

      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ emails: mockEmails }),
      })
      setToken('test-token')

      const result = await listEmails()

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/emails')
      expect(result).toEqual(mockEmails)
    })
  })

  describe('getEmail', () => {
    it('fetches single email with auth header', async () => {
      const mockEmail = { id: '1', subject: 'Test', from: { address: 'a@b.com' }, to: [], date: '', text: '', read: false }

      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockEmail),
      })
      setToken('test-token')

      const result = await getEmail('1')

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/emails/1')
      expect(result).toEqual(mockEmail)
    })

    it('encodes email ID', async () => {
      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ id: 'id-with/slashes' }),
      })
      setToken('test-token')

      await getEmail('id-with/slashes')

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/emails/id-with%2Fslashes')
    })
  })

  describe('markUnread', () => {
    it('posts to unread endpoint', async () => {
      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ok: true }),
      })
      setToken('test-token')

      await markUnread('email-1')

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/emails/email-1/unread')
      expect(callArgs[1]).toMatchObject({ method: 'POST' })
    })
  })

  describe('sendEmail', () => {
    it('posts to send endpoint with message', async () => {
      mockFetch.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ok: true }),
      })
      setToken('test-token')

      await sendEmail({
        to: ['a@b.com'],
        cc: ['c@d.com'],
        subject: 'Subject',
        text: 'Body',
      })

      expect(mockFetch).toHaveBeenCalled()
      const callArgs = mockFetch.mock.calls[0]
      expect(callArgs[0]).toBe('/api/send')
      expect(callArgs[1]).toMatchObject({ method: 'POST' })
    })
  })

  describe('errorMessage', () => {
    it('returns ApiError message', () => {
      expect(errorMessage(new ApiError('API error', 400))).toBe('API error')
    })

    it('returns Error message', () => {
      expect(errorMessage(new Error('Generic error'))).toBe('Generic error')
    })

    it('returns fallback for unknown error', () => {
      expect(errorMessage('string error')).toBe('Something went wrong')
      expect(errorMessage(null)).toBe('Something went wrong')
      expect(errorMessage(undefined)).toBe('Something went wrong')
      expect(errorMessage({})).toBe('Something went wrong')
    })
  })
})