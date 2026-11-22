import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import MasqueradeModal from './MasqueradeModal.tsx';

vi.mock('../api.ts', () => ({
  autocompleteUsers: vi.fn(async () => ({ users: [] })),
}));

describe('MasqueradeModal', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let onClose: () => void;
  let onSelect: (email: string) => void;

  const render = async (open = true) => {
    await act(async () => {
      root!.render(<MasqueradeModal open={open} onClose={onClose} onSelect={onSelect} />);
    });
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    onClose = vi.fn(() => {});
    onSelect = vi.fn(() => {});
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    document.body.replaceChildren();
  });

  it('closes when the backdrop is clicked', async () => {
    await render();
    await act(async () => {
      container.querySelector('.modal-overlay')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('does not close when clicking inside the modal card', async () => {
    await render();
    await act(async () => {
      container.querySelector('.modal-card')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    expect(onClose).not.toHaveBeenCalled();
  });

  it('closes on Escape like Cancel', async () => {
    await render();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
