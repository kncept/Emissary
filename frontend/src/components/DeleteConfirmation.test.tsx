import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { deleteEmail, deleteUser, getUsers } from '../api.ts';
import EmailList from './EmailList.tsx';
import EmailView from './EmailView.tsx';
import Users from './Users.tsx';

vi.mock('../api.ts', () => ({
  getEmails: vi.fn(async () => [{
    id: 'users/alice@example.com/inbox/e1.eml',
    from: 'alice@example.com',
    to: 'bob@example.com',
    subject: 'Hello',
    body: 'Body',
    timestamp: '2026-11-11T00:00:00Z',
    read: false,
    flagged: false,
  }]),
  getEmail: vi.fn(async () => ({
    id: 'users/alice@example.com/inbox/e1.eml',
    from: 'alice@example.com',
    to: 'bob@example.com',
    subject: 'Hello',
    body: 'Body',
    timestamp: '2026-11-11T00:00:00Z',
    read: false,
    flagged: false,
  })),
  getUsers: vi.fn(async () => ({ users: [{ email: 'alice@example.com', enabled: true, admin: false, rules: [] }] })),
  deleteEmail: vi.fn(async () => {}),
  deleteUser: vi.fn(async () => ({})),
  markRead: vi.fn(),
  markFlagged: vi.fn(),
}));

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function cancelByBackdrop() {
  const overlay = document.querySelector('.modal-overlay');
  expect(overlay).not.toBeNull();
  click(overlay!);
  expect(document.querySelector('[role="dialog"]')).toBeNull();
}

describe('delete confirmations', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = (ui: ReactNode) => {
    act(() => {
      root!.render(ui);
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
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

  it('confirms inbox deletions in an in-app modal', async () => {
    mount(<MemoryRouter><EmailList /></MemoryRouter>);
    await act(async () => {});

    const deleteButton = container.querySelector('[aria-label="Delete email"]')!;
    click(deleteButton);
    expect(document.querySelector('[role="dialog"]')!.textContent).toContain('Delete this email?');

    cancelByBackdrop();
    expect(deleteEmail).not.toHaveBeenCalled();

    click(deleteButton);
    click(document.querySelector('.modal-footer .btn-danger')!);
    await act(async () => {});
    expect(deleteEmail).toHaveBeenCalledWith('users/alice@example.com/inbox/e1.eml');
  });

  it('confirms message-view deletions in an in-app modal', async () => {
    mount(
      <MemoryRouter initialEntries={[`/email/${encodeURIComponent('users/alice@example.com/inbox/e1.eml')}`]}>
        <Routes>
          <Route path="/email/:id" element={<EmailView />} />
        </Routes>
      </MemoryRouter>,
    );
    await act(async () => {});

    const deleteButton = Array.from(container.querySelectorAll('button')).find(
      (button) => button.textContent === 'Delete',
    )!;
    click(deleteButton);
    expect(document.querySelector('[role="dialog"]')!.textContent).toContain('Delete this email?');

    cancelByBackdrop();
    expect(deleteEmail).not.toHaveBeenCalled();

    click(deleteButton);
    click(document.querySelector('.modal-footer .btn-danger')!);
    await act(async () => {});
    expect(deleteEmail).toHaveBeenCalledWith('users/alice@example.com/inbox/e1.eml');
  });

  it('confirms user deletions in an in-app modal', async () => {
    mount(<Users />);
    await act(async () => {});

    const deleteButton = Array.from(container.querySelectorAll('button')).find(
      (button) => button.textContent === 'Delete',
    )!;
    click(deleteButton);
    expect(document.querySelector('[role="dialog"]')!.textContent).toContain('Delete user alice@example.com?');

    cancelByBackdrop();
    expect(deleteUser).not.toHaveBeenCalled();

    click(deleteButton);
    click(document.querySelector('.modal-footer .btn-danger')!);
    await act(async () => {});
    expect(deleteUser).toHaveBeenCalledWith('alice@example.com');
    expect(getUsers).toHaveBeenCalledTimes(2);
  });
});
