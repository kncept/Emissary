import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, type ComponentProps } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ConfirmDialog from './ConfirmDialog.tsx';

describe('ConfirmDialog', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const renderDialog = (overrides: Partial<ComponentProps<typeof ConfirmDialog>> = {}) => {
    const props = {
      open: true,
      title: 'Delete email',
      message: 'Delete this email? This cannot be undone.',
      onConfirm: vi.fn(),
      onCancel: vi.fn(),
      ...overrides,
    };
    act(() => {
      root!.render(<ConfirmDialog {...props} />);
    });
    return props;
  };

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
  });

  it('renders the message and confirms only via the destructive button', () => {
    const { onConfirm, onCancel } = renderDialog();
    expect(container.textContent).toContain('Delete this email? This cannot be undone.');

    const confirm = Array.from(container.querySelectorAll('button')).find(
      (button) => button.textContent === 'Delete',
    )!;
    act(() => {
      confirm.click();
    });
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('cancels when the backdrop is clicked', () => {
    const { onCancel, onConfirm } = renderDialog();
    act(() => {
      container.querySelector('.modal-overlay')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('does not cancel when the dialog card is clicked', () => {
    const { onCancel } = renderDialog();
    act(() => {
      container.querySelector('.modal-body')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('cancels on Escape', () => {
    const { onCancel } = renderDialog();
    act(() => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('blocks dismissal and confirmation while busy', () => {
    const { onCancel, onConfirm } = renderDialog({ busy: true });
    act(() => {
      container.querySelector('.modal-overlay')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(onCancel).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(container.querySelector<HTMLButtonElement>('.btn-danger')!.disabled).toBe(true);
  });
});
