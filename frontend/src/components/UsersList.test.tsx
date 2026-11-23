import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { getUsers } from '../api.ts';
import Users from './Users.tsx';

vi.mock('../api.ts', () => ({
  getUsers: vi.fn(),
  createUser: vi.fn(async () => ({})),
  updateUser: vi.fn(async () => ({})),
  deleteUser: vi.fn(async () => ({})),
  changeUserPassword: vi.fn(async () => ({})),
}));

const user = (email: string) => ({
  email,
  enabled: true,
  systemManager: false,
  userManager: false,
  rules: [],
});

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

describe('user list pagination', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    vi.mocked(getUsers).mockReset();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    container.remove();
  });

  const render = async () => {
    act(() => root!.render(<Users currentUser="admin@example.com" />));
    await act(async () => {});
  };

  const loadMore = () => {
    const button = Array.from(container.querySelectorAll('button')).find(b => b.textContent === 'Load more');
    expect(button).toBeDefined();
    click(button!);
  };

  const emails = () =>
    Array.from(container.querySelectorAll('tbody tr')).map(row => row.querySelector('td')?.textContent);

  it('does not show the same user twice when a page repeats it', async () => {
    vi.mocked(getUsers)
      .mockResolvedValueOnce({ users: [user('alice@example.com')], nextCursor: 'cursor-2' })
      .mockResolvedValueOnce({ users: [user('alice@example.com'), user('bob@example.com')] });

    await render();
    loadMore();
    await act(async () => {});

    expect(emails()).toEqual(['alice@example.com', 'bob@example.com']);
  });

  it('ignores another Load more click while a page is loading', async () => {
    let resolvePage: (value: { users: ReturnType<typeof user>[] }) => void = () => {};
    vi.mocked(getUsers)
      .mockResolvedValueOnce({ users: [user('alice@example.com')], nextCursor: 'cursor-2' })
      .mockImplementationOnce(() => new Promise(resolve => { resolvePage = resolve; }));

    await render();
    loadMore();
    loadMore();
    expect(getUsers).toHaveBeenCalledTimes(2);

    await act(async () => {
      resolvePage({ users: [user('bob@example.com')] });
    });

    expect(emails()).toEqual(['alice@example.com', 'bob@example.com']);
  });
});
