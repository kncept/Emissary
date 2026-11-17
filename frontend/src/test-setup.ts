// Global setup for vitest, run before every test file.
// The jsdom environment is configured in vitest.config.ts; add
// shared mocks or polyfills here as needed.

// Tell React that tests run inside act() so rendering tests can use it
// without the "not configured to support act" warning.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// There must be no native browser dialogs in the app. Any test that
// reaches one fails loudly instead of hanging on a jsdom stub.
for (const method of ['alert', 'confirm', 'prompt'] as const) {
  Object.defineProperty(window, method, {
    configurable: true,
    writable: true,
    value: () => {
      throw new Error(`window.${method} is forbidden; use an in-app dialog instead.`);
    },
  });
}
