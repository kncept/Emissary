import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import Login from './Login'
import { mockFetch } from './test-setup'

describe('Login', () => {
  const mockOnLogin = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    mockOnLogin.mockClear()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders username and password fields', () => {
    render(<Login onLogin={mockOnLogin} />)

    expect(screen.getByLabelText('Username')).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  it('calls onLogin on successful submit', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({
        token: 'token',
        tokenType: 'Bearer',
        expiresAt: new Date().toISOString(),
      }),
    })

    render(<Login onLogin={mockOnLogin} />)

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pass' } })
    fireEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    await waitFor(() => expect(mockOnLogin).toHaveBeenCalled())
  })

  it('shows error on failed login', async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 401,
      json: () => Promise.resolve({ error: 'Invalid credentials' }),
    })

    render(<Login onLogin={mockOnLogin} />)

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pass' } })
    fireEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Invalid credentials'))
  })

  it('disables button while submitting', async () => {
    let resolveFetch: (value: any) => void
    const fetchPromise = new Promise((resolve) => { resolveFetch = resolve })
    mockFetch.mockReturnValue(fetchPromise)

    render(<Login onLogin={mockOnLogin} />)

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'user' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pass' } })
    fireEvent.click(screen.getByRole('button', { name: /sign in/i }))

    expect(screen.getByRole('button', { name: /signing in/i })).toBeDisabled()

    resolveFetch!({
      ok: true,
      json: () => Promise.resolve({
        token: 'token',
        tokenType: 'Bearer',
        expiresAt: new Date().toISOString(),
      }),
    })

    await waitFor(() => expect(mockOnLogin).toHaveBeenCalled())
  })
})