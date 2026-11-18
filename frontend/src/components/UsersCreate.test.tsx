import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { createUser } from '../api.ts';
import Users from './Users.tsx';

vi.mock('../api.ts', () => ({
  getUsers: vi.fn(async () => ({ users: [] })),
  createUser: vi.fn(async () => ({})),
  updateUser: vi.fn(async () => ({})),
  deleteUser: vi.fn(async () => ({})),
  changeUserPassword: vi.fn(async () => ({})),
}));

function click(element: Element) {
  act(() => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function setValue(input: HTMLInputElement, value: string) {
  act(() => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    setter.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

describe('create user with no password', () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

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

  const openCreateForm = async () => {
    act(() => root!.render(<Users />));
    await act(async () => {});
    click(Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Create User')!);
  };

  const noPasswordCheckbox = () =>
    Array.from(container.querySelectorAll('label')).find((label) => label.textContent === 'No password')!.querySelector('input')!;

  it('disables the password field and marks it as unable to log in when checked', async () => {
    await openCreateForm();
    const password = container.querySelector<HTMLInputElement>('#create-password')!;
    expect(password.disabled).toBe(false);
    expect(password.required).toBe(true);

    click(noPasswordCheckbox());

    expect((noPasswordCheckbox() as HTMLInputElement).checked).toBe(true);
    expect(password.disabled).toBe(true);
    expect(password.required).toBe(false);
    expect(password.placeholder).toBe('cannot log in');
    expect(password.value).toBe('cannot log in');
    expect(container.textContent).toContain('cannot log in');

    click(noPasswordCheckbox());
    expect(password.disabled).toBe(false);
    expect(password.required).toBe(true);
  });

  it('submits a blank password for a no-password user', async () => {
    await openCreateForm();
    setValue(container.querySelector<HTMLInputElement>('#create-email')!, 'nopass@example.com');
    setValue(container.querySelector<HTMLInputElement>('#create-password')!, 'typed-but-ignored');
    click(noPasswordCheckbox());
    expect((container.querySelector<HTMLInputElement>('#create-password')!).value).toBe('cannot log in');

    act(() => {
      container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    await act(async () => {});

    expect(createUser).toHaveBeenCalledWith(expect.objectContaining({
      email: 'nopass@example.com',
      password: '',
    }));
  });
});
