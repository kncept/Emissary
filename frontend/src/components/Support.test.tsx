import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import Support from './Support.tsx';

vi.mock('../api.ts', () => ({
  getSupportConfigurations: vi.fn(async () => ({
    configurations: [
      { name: 'receiver', implementation: 'memory', description: 'Inbound recipient routing rules' },
      { name: 'storage', implementation: 'memory', description: 'Email storage backend' },
    ],
  })),
  getSupportConfiguration: vi.fn(async (name: string) => ({
    name,
    implementation: 'memory',
    description: 'Configuration details',
    settings: { type: 'memory' },
  })),
  getSupportBucketRoot: vi.fn(async () => ({ bucket: 'mail', objects: [], prefixes: [] })),
  processSupportEmail: vi.fn(),
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

describe('Support receiver edit link', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const settle = async () => {
    await act(async () => {});
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root!.render(
        <MemoryRouter>
          <Support />
        </MemoryRouter>,
      );
    });
  });

  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();
  });

  it('links to the receiver editor only from the receiver details', async () => {
    await settle();
    const viewButtons = Array.from(container.querySelectorAll('button')).filter((node) => node.textContent === 'View');
    expect(viewButtons.length).toBe(2);

    click(viewButtons[1]); // storage — no edit button
    await settle();
    expect(container.querySelector('a[href="/support/receiver"]')).toBeNull();

    click(buttonByText(container, 'Close'));
    const receiverView = Array.from(container.querySelectorAll('button')).find((node) => node.textContent === 'View');
    click(receiverView!);
    await settle();

    const edit = container.querySelector('a[href="/support/receiver"]');
    expect(edit?.textContent).toBe('Edit');
  });
});
