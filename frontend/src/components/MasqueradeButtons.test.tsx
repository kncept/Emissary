import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { setMasqueradeTarget } from '../masquerade.ts';
import Layout from './Layout.tsx';
import Users from './Users.tsx';

vi.mock('../api.ts', () => ({
  getUsers: vi.fn(async () => ({
    users: [{ email: 'alice@example.com', enabled: true, systemManager: false, userManager: false, rules: [] }],
  })),
  createUser: vi.fn(async () => ({})),
  updateUser: vi.fn(async () => ({})),
  deleteUser: vi.fn(async () => ({})),
  changeUserPassword: vi.fn(async () => ({})),
  autocompleteUsers: vi.fn(async () => ({ users: [] })),
}));

vi.mock('../masquerade.ts', () => ({
  setMasqueradeTarget: vi.fn(),
  clearMasquerade: vi.fn(),
}));

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function maskIcon(button: Element) {
  return button.querySelector('use')?.getAttribute('href');
}

describe('masquerade buttons', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const mount = (ui: React.ReactNode) => {
    act(() => root!.render(ui));
  };

  beforeEach(() => {
    vi.clearAllMocks();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    container.remove();
  });

  it('offers a masquerade action for each listed user', async () => {
    mount(<Users />);
    await act(async () => {});

    const button = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Masquerade'),
    )!;
    expect(button.getAttribute('aria-label')).toBe('Masquerade as alice@example.com');
    expect(button.getAttribute('title')).toBe('Masquerade as alice@example.com');
    expect(maskIcon(button)).toBe('/icons.svg#mask-icon');

    click(button);
    expect(setMasqueradeTarget).toHaveBeenCalledWith('alice@example.com');
  });

  it('uses the mask icon on the header masquerade button', () => {
    mount(
      <MemoryRouter>
        <Layout onLogout={() => {}} canMasquerade />
      </MemoryRouter>,
    );

    const button = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Masquerade'),
    )!;
    expect(maskIcon(button)).toBe('/icons.svg#mask-icon');
  });
});
