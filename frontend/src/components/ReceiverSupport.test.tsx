import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import {
  deleteSupportReceiverRule,
  getSupportReceiverIdentities,
  getSupportReceiverRules,
  saveSupportReceiverRules,
} from '../api.ts';
import type { ReceiverRulesRequest } from '../api/generated/schemas/support/index.ts';
import ReceiverSupport from './ReceiverSupport.tsx';

vi.mock('../api.ts', () => ({
  deleteSupportReceiverRule: vi.fn(),
  getSupportReceiverIdentities: vi.fn(),
  getSupportReceiverRules: vi.fn(),
  saveSupportReceiverRules: vi.fn(),
}));

const baseRules: ReceiverRulesRequest['rules'] = [
  { domain: '_', rules: ['DeliverToUser', 'Bounce'] },
];

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((node) => node.textContent === text);
  if (!button) throw new Error(`button "${text}" not found`);
  return button as HTMLButtonElement;
}

describe('Receiver rules editor', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const settle = async () => {
    await act(async () => {});
  };

  const render = () => {
    act(() => {
      root!.render(
        <MemoryRouter initialEntries={['/support/receiver']}>
          <Routes>
            <Route path="/support/receiver" element={<ReceiverSupport />} />
            <Route path="/support" element={<div>Support home</div>} />
          </Routes>
        </MemoryRouter>,
      );
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getSupportReceiverIdentities).mockResolvedValue({
      identities: [
        { identity: 'example.com', hasRules: true },
        { identity: 'news.example.com', hasRules: false },
      ],
    });
    vi.mocked(getSupportReceiverRules).mockResolvedValue({ rules: baseRules });
    vi.mocked(saveSupportReceiverRules).mockImplementation(async (rules) => ({ rules }));
    vi.mocked(deleteSupportReceiverRule).mockImplementation(async () => ({ rules: baseRules }));

    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();
  });

  it('shows configured identities with a tick and unconfigured ones with a plus', async () => {
    render();
    await settle();

    const plus = container.querySelector('button[title="Add rules for news.example.com"]');
    expect(plus?.textContent).toBe('+');
    expect(container.querySelector('button[title="Add rules for example.com"]')).toBeNull();
    expect(container.querySelector('.receiver-tick')?.textContent).toBe('✓');
  });

  it('shows one flat list of rules per identity with no mailbox or action fields', async () => {
    render();
    await settle();

    const headings = Array.from(container.querySelectorAll('h3')).map((node) => node.textContent);
    expect(headings).toContain('Receiving mail identities');
    expect(headings).not.toContain('AWS SES identities');
    expect(container.textContent).not.toContain('Mailbox identity');
    expect(container.textContent).not.toContain('Add handler');
    expect(container.textContent).not.toContain('Action 1');
    expect(container.querySelector('.receiver-domain-header code')?.textContent).toBe('_');

    const domains = container.querySelectorAll('.receiver-domain');
    expect(domains).toHaveLength(1);
    expect(domains[0].querySelectorAll('.receiver-rule-list')).toHaveLength(1);
    expect(domains[0].querySelectorAll('.receiver-rule-row select')).toHaveLength(2);
    expect(domains[0].querySelector('input')).toBeNull();
    expect(container.querySelector('select[aria-label="Rule 1 for _"]')).not.toBeNull();
    expect(container.querySelector('select[aria-label="Action 1 for _"]')).toBeNull();
  });

  it('shows configured rules even when the SES identities call fails', async () => {
    vi.mocked(getSupportReceiverIdentities).mockRejectedValue(new Error('failed to list SES identities'));
    vi.mocked(getSupportReceiverRules).mockResolvedValue({
      rules: [
        ...baseRules,
        { domain: 'example.com', rules: ['DeliverToUser', 'Bounce'] },
      ],
    });
    render();
    await settle();

    expect(container.querySelectorAll('.receiver-domain')).toHaveLength(2);
    expect(container.textContent).toContain('example.com');
    expect(container.textContent).toContain('failed to list SES identities');
  });

  it('adds rules for a domain from the plus and saves them with OK', async () => {
    render();
    await settle();

    click(container.querySelector('button[title="Add rules for news.example.com"]')!);
    expect(container.textContent).toContain('news.example.com');

    click(buttonByText(container, 'OK'));
    await settle();

    expect(saveSupportReceiverRules).toHaveBeenCalledTimes(1);
    const saved = vi.mocked(saveSupportReceiverRules).mock.calls[0][0];
    const added = saved.find((rule) => rule.domain === 'news.example.com');
    expect(added?.rules).toEqual(['DeliverToUser', 'Bounce']);
    expect(added).not.toHaveProperty('handlers');
  });

  it('reorders, changes and removes rules using the flat list controls', async () => {
    render();
    await settle();

    const selects = container.querySelectorAll<HTMLSelectElement>('.receiver-rule-row select');
    expect(Array.from(selects).map((node) => node.value)).toEqual(['DeliverToUser', 'Bounce']);
    expect(Array.from(selects[0].options).map((option) => option.value)).toContain('Drop');

    // Move Bounce above DeliverToUser.
    click(container.querySelector('button[title="Move rule up"]:not([disabled])')!);
    expect(Array.from(container.querySelectorAll<HTMLSelectElement>('.receiver-rule-row select'))
      .map((node) => node.value)).toEqual(['Bounce', 'DeliverToUser']);

    // Change the second rule.
    const second = container.querySelectorAll<HTMLSelectElement>('.receiver-rule-row select')[1];
    act(() => {
      second.value = 'DeliverToMailIdentity';
      second.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(Array.from(container.querySelectorAll<HTMLSelectElement>('.receiver-rule-row select'))
      .map((node) => node.value)).toEqual(['Bounce', 'DeliverToMailIdentity']);

    // Remove Bounce; only one rule must remain.
    click(container.querySelector('button[title="Remove rule"]')!);
    expect(container.querySelectorAll('.receiver-rule-row select')).toHaveLength(1);
    expect((container.querySelector('.receiver-rule-row select') as HTMLSelectElement).value)
      .toBe('DeliverToMailIdentity');

    click(buttonByText(container, 'OK'));
    await settle();
    const saved = vi.mocked(saveSupportReceiverRules).mock.calls[0][0];
    expect(saved.find((rule) => rule.domain === '_')?.rules).toEqual(['DeliverToMailIdentity']);
  });

  it('edits a saved DeliverTo target and validates it before saving', async () => {
    render();
    await settle();

    const select = container.querySelector<HTMLSelectElement>('select[aria-label="Rule 1 for _"]')!;
    act(() => {
      select.value = 'DeliverTo:';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });

    const input = container.querySelector<HTMLInputElement>('input[aria-label="Delivery address for rule 1 for _"]')!;
    expect(input).not.toBeNull();
    expect(input.value).toBe('');

    click(buttonByText(container, 'OK'));
    await settle();
    expect(saveSupportReceiverRules).not.toHaveBeenCalled();
    expect(container.textContent).toContain('Enter a valid user address');

    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    act(() => {
      setValue.call(input, 'unknown@kncept.com');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    click(buttonByText(container, 'OK'));
    await settle();
    expect(saveSupportReceiverRules).toHaveBeenCalledTimes(1);
    const saved = vi.mocked(saveSupportReceiverRules).mock.calls[0][0];
    expect(saved.find((rule) => rule.domain === '_')?.rules)
      .toEqual(['DeliverTo:unknown@kncept.com', 'Bounce']);
  });

  it('shows the saved address in an existing DeliverTo rule', async () => {
    vi.mocked(getSupportReceiverRules).mockResolvedValue({
      rules: [{ domain: '_', rules: ['DeliverTo:catchall@kncept.com', 'Bounce'] }],
    });
    render();
    await settle();

    expect((container.querySelector('select[aria-label="Rule 1 for _"]') as HTMLSelectElement).value)
      .toBe('DeliverTo:');
    expect((container.querySelector('input[aria-label="Delivery address for rule 1 for _"]') as HTMLInputElement).value)
      .toBe('catchall@kncept.com');
  });

  it('discards unsaved edits on Reload and navigates back on Cancel', async () => {
    render();
    await settle();

    click(container.querySelector('button[title="Add rules for news.example.com"]')!);
    expect(container.textContent).toContain('news.example.com');
    click(buttonByText(container, 'Reload'));
    await settle();
    expect(container.querySelectorAll('.receiver-domain')).toHaveLength(1);
    expect(container.querySelector('.receiver-domain-header code')?.textContent).toBe('_');

    click(buttonByText(container, 'Cancel'));
    expect(container.textContent).toContain('Support home');
  });

  it('never allows removing the fallback definition', async () => {
    render();
    await settle();

    const remove = container.querySelector('button[title="The fallback rules cannot be removed"]');
    expect(remove).not.toBeNull();
    expect((remove as HTMLButtonElement).disabled).toBe(true);
    click(remove!);
    expect(deleteSupportReceiverRule).not.toHaveBeenCalled();
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });

  it('drops an unsaved identity locally without calling DELETE', async () => {
    render();
    await settle();

    click(container.querySelector('button[title="Add rules for news.example.com"]')!);
    expect(container.querySelectorAll('.receiver-domain')).toHaveLength(2);

    click(container.querySelector('button[title="Remove rules for news.example.com"]')!);
    const confirm = Array.from(container.querySelectorAll('[role="dialog"] button'))
      .find((node) => node.textContent === 'Remove');
    click(confirm!);
    await settle();

    expect(deleteSupportReceiverRule).not.toHaveBeenCalled();
    expect(container.querySelectorAll('.receiver-domain')).toHaveLength(1);
    expect(container.querySelector('button[title="Add rules for news.example.com"]')).not.toBeNull();
  });

  it('removes a configured identity after confirmation and shows its plus again', async () => {
    vi.mocked(getSupportReceiverRules).mockResolvedValue({
      rules: [
        ...baseRules,
        { domain: 'example.com', rules: ['DeliverToUser', 'Bounce'] },
      ],
    });
    render();
    await settle();

    click(container.querySelector('button[title="Remove rules for example.com"]')!);
    const dialog = container.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('example.com');
    const confirm = Array.from(dialog!.querySelectorAll('button')).find((node) => node.textContent === 'Remove');
    click(confirm!);
    await settle();

    expect(deleteSupportReceiverRule).toHaveBeenCalledWith('example.com');
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(container.querySelectorAll('.receiver-domain')).toHaveLength(1);
    expect(container.querySelector('button[title="Add rules for example.com"]')).not.toBeNull();
  });
});
