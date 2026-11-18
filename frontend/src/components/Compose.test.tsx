import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { sendEmail } from '../api.ts';
import Compose from './Compose.tsx';

vi.mock('../api.ts', () => ({
  getEmail: vi.fn(),
  sendEmail: vi.fn(async () => ({ success: true })),
}));

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((node) => node.textContent === text);
  if (!button) throw new Error(`button "${text}" not found`);
  return button as HTMLButtonElement;
}

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

describe('Compose body format', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root!.render(
        <MemoryRouter>
          <Compose />
        </MemoryRouter>,
      );
    });
  });

  afterEach(() => {
    act(() => root!.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('defaults to the HTML editor with a minimal formatting toolbar', () => {
    expect(container.querySelector('.rsw-ce')).not.toBeNull();
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelectorAll('.rsw-toolbar .rsw-btn').length).toBe(6);
    expect(buttonByText(container, 'HTML').getAttribute('aria-pressed')).toBe('true');
  });

  it('switches between plain text and HTML without losing either draft', () => {
    click(buttonByText(container, 'Plain text'));
    expect(container.querySelector('textarea#body')).not.toBeNull();
    expect(container.querySelector('.rsw-ce')).toBeNull();
    expect(buttonByText(container, 'Plain text').getAttribute('aria-pressed')).toBe('true');

    click(buttonByText(container, 'HTML'));
    expect(container.querySelector('textarea#body')).toBeNull();
    expect(container.querySelector('.rsw-ce')).not.toBeNull();
  });

  it('rejects an empty message body', () => {
    act(() => {
      container.querySelector('form')!.dispatchEvent(
        new Event('submit', { bubbles: true, cancelable: true }),
      );
    });
    expect(sendEmail).not.toHaveBeenCalled();
    expect(container.textContent).toContain('Please enter a message body');
  });
});
