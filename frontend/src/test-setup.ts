import '@testing-library/jest-dom'
import { vi, beforeEach } from 'vitest'

// Mock localStorage properly
const localStorageStore: Record<string, string> = {}

Object.defineProperty(window, 'localStorage', {
  value: {
    getItem: vi.fn((key: string) => localStorageStore[key] ?? null),
    setItem: vi.fn((key: string, value: string) => { localStorageStore[key] = value }),
    removeItem: vi.fn((key: string) => { delete localStorageStore[key] }),
    clear: vi.fn(() => { Object.keys(localStorageStore).forEach(k => delete localStorageStore[k]) }),
  },
  writable: true,
})

// Mock window.dispatchEvent
window.dispatchEvent = vi.fn()

// Mock fetch globally
const mockFetch = vi.fn()
global.fetch = mockFetch

beforeEach(() => {
  vi.clearAllMocks()
  Object.keys(localStorageStore).forEach(k => delete localStorageStore[k])
  mockFetch.mockReset()
})

export { mockFetch }