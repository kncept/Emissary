import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { getVacationResponse, setVacationResponse } from '../api.ts';
import type { VacationResponse } from '../api/generated/schemas/jmap';
import VacationSettings from './VacationSettings.tsx';

vi.mock('../api.ts', () => ({
  getVacationResponse: vi.fn(),
  setVacationResponse: vi.fn(),
}));

const disabledResponse: VacationResponse = { id: 'singleton', isEnabled: false };

const configuredResponse: VacationResponse = {
  id: 'singleton',
  isEnabled: true,
  fromDate: '2026-01-01T00:00:00Z',
  toDate: null,
  subject: 'Away',
  textBody: 'Back soon',
  htmlBody: '<p>Back soon</p>',
};

function setValue(element: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const prototype =
    element instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(prototype, 'value')!.set!;
  setter.call(element, value);
  element.dispatchEvent(new Event('input', { bubbles: true }));
}

describe('VacationSettings modal', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let onClose: () => void;

  const render = async () => {
    await act(async () => {
      root!.render(<VacationSettings onClose={onClose} />);
    });
  };

  const field = (selector: string) => container.querySelector(selector) as HTMLInputElement | HTMLTextAreaElement;
  const button = (label: string) =>
    Array.from(container.querySelectorAll('button')).find((item) => item.textContent?.includes(label))!;
  const formatButton = (label: string) =>
    Array.from(container.querySelectorAll('[aria-label="Body format"] button')).find(
      (item) => item.textContent === label,
    )!;
  const statusButton = (label: string) =>
    Array.from(
      container.querySelectorAll('[aria-labelledby="vacation-enabled-label"] button'),
    ).find((item) => item.textContent === label)!;

  const click = async (element: Element) => {
    await act(async () => {
      element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    onClose = vi.fn(() => {});
    vi.mocked(getVacationResponse).mockReset();
    vi.mocked(setVacationResponse).mockReset();
    vi.mocked(getVacationResponse).mockResolvedValue(disabledResponse);
    vi.mocked(setVacationResponse).mockImplementation(async (update) => ({
      id: 'singleton',
      ...update,
    }));
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    document.body.replaceChildren();
  });

  it('loads the current settings and saves the full singleton', async () => {
    vi.mocked(getVacationResponse).mockResolvedValue(configuredResponse);
    await render();

    expect(container.querySelector('[role="dialog"]')?.getAttribute('aria-labelledby')).toBe('vacation-title');
    expect(container.querySelector('#vacation-enabled-label')?.textContent).toBe('Automatic reply');
    expect(container.querySelector('input[type="checkbox"]')).toBeNull();
    expect(getVacationResponse).toHaveBeenCalledTimes(1);
    expect(statusButton('Enabled').getAttribute('aria-pressed')).toBe('true');
    expect(field('#vacation-subject').value).toBe('Away');
    expect(field('#vacation-body').value).toBe('Back soon');

    await click(statusButton('Disabled'));
    await act(async () => {
      setValue(field('#vacation-subject'), 'Back Monday');
      setValue(field('#vacation-body'), 'Back on Monday.');
    });

    await click(button('Save vacation response'));

    expect(setVacationResponse).toHaveBeenCalledTimes(1);
    const update = vi.mocked(setVacationResponse).mock.calls[0][0];
    expect(update.isEnabled).toBe(false);
    expect(new Date(update.fromDate as string).toISOString()).toBe('2026-01-01T00:00:00.000Z');
    expect(update.toDate).toBeNull();
    expect(update.subject).toBe('Back Monday');
    expect(update.textBody).toBe('Back on Monday.');
    // The HTML body loaded with the settings is preserved while editing plain text.
    expect(update.htmlBody).toBe('<p>Back soon</p>');
    expect(container.textContent).toContain('Vacation response saved.');
  });

  it('uses the shared Compose body editor with selectable formats', async () => {
    await render();
    expect(container.querySelector('#vacation-body')).not.toBeNull();
    expect(formatButton('Plain text').getAttribute('aria-pressed')).toBe('true');

    await click(formatButton('HTML'));

    expect(formatButton('HTML').getAttribute('aria-pressed')).toBe('true');
    expect(container.querySelector('#vacation-body')).toBeNull();
    expect(container.querySelector('.rsw-editor')).not.toBeNull();

    await click(formatButton('Plain text'));
    expect(container.querySelector('#vacation-body')).not.toBeNull();
  });

  it('rejects a date range that ends before it starts', async () => {
    await render();
    await act(async () => {
      setValue(field('#vacation-from'), '2026-02-01T09:00');
      setValue(field('#vacation-to'), '2026-01-01T09:00');
    });

    await click(button('Save vacation response'));

    expect(container.textContent).toContain('From date must be before to date.');
    expect(setVacationResponse).not.toHaveBeenCalled();
  });

  it('shows load errors and retries', async () => {
    vi.mocked(getVacationResponse)
      .mockRejectedValueOnce(new Error('session unavailable'))
      .mockResolvedValueOnce(configuredResponse);
    await render();

    expect(container.textContent).toContain('session unavailable');
    expect(container.querySelector('#vacation-subject')).toBeNull();

    await click(button('Retry'));

    expect(getVacationResponse).toHaveBeenCalledTimes(2);
    expect(field('#vacation-subject').value).toBe('Away');
  });

  it('shows save errors from the JMAP notUpdated response', async () => {
    await render();
    vi.mocked(setVacationResponse).mockRejectedValueOnce(new Error('invalidProperties: bad date'));

    await click(button('Save vacation response'));

    expect(container.textContent).toContain('invalidProperties: bad date');
  });

  it('closes from the close button and Escape', async () => {
    await render();

    await click(container.querySelector('[aria-label="Close vacation response"]')!);
    expect(onClose).toHaveBeenCalledTimes(1);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
