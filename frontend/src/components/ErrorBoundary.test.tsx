import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ErrorBoundary from './ErrorBoundary.tsx';

function Bomb({ fail }: { fail: boolean }) {
  if (fail) {
    throw new Error('boom');
  }
  return <div>Recovered</div>;
}

describe('ErrorBoundary', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const click = (element: Element) => {
    act(() => {
      element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  };
  const retryButton = () =>
    Array.from(container.querySelectorAll('button')).find((button) => button.textContent === 'Try again')!;

  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
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
    vi.restoreAllMocks();
  });

  it('contains a rendering failure and keeps sibling UI alive', () => {
    act(() => {
      root!.render(
        <>
          <span>Header</span>
          <ErrorBoundary><Bomb fail /></ErrorBoundary>
        </>,
      );
    });

    expect(container.textContent).toContain('Something went wrong');
    expect(container.textContent).toContain('Header');
  });

  it('lets the user retry after the failure is resolved', () => {
    act(() => {
      root!.render(<ErrorBoundary><Bomb fail /></ErrorBoundary>);
    });
    expect(container.textContent).toContain('Something went wrong');

    // The failure is gone: give the boundary healthy children, then retry.
    act(() => {
      root!.render(<ErrorBoundary><Bomb fail={false} /></ErrorBoundary>);
    });
    click(retryButton());

    expect(container.textContent).toContain('Recovered');
    expect(container.textContent).not.toContain('Something went wrong');
  });
});
