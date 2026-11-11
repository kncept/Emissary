// Global setup for vitest, run before every test file.
// The jsdom environment is configured in vitest.config.ts; add
// shared mocks or polyfills here as needed.

// Tell React that tests run inside act() so rendering tests can use it
// without the "not configured to support act" warning.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
