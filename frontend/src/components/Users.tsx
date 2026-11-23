import { useEffect, useRef, useState } from 'react';
import { getUsers, createUser, updateUser, deleteUser, changeUserPassword } from '../api.ts';
import ConfirmDialog from './ConfirmDialog.tsx';
import MaskIcon from './MaskIcon.tsx';
import { setMasqueradeTarget } from '../masquerade.ts';

interface User {
  email: string;
  enabled: boolean;
  systemManager: boolean;
  userManager: boolean;
  rules: string[];
}

// Email addresses identify users, so keep one row per address even if a page
// is retried or contains differently-cased variants of the same address.
function uniqueUsers(list: User[]): User[] {
  const seen = new Set<string>();
  return list.filter((user) => {
    const key = user.email.toLowerCase();
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export default function Users({ currentUser }: { currentUser?: string }) {
  const [users, setUsers] = useState<User[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const loadingMoreRef = useRef(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');

  const [showCreate, setShowCreate] = useState(false);
  const [createEmail, setCreateEmail] = useState('');
  const [createPassword, setCreatePassword] = useState('');
  const [createNoPassword, setCreateNoPassword] = useState(false);
  const [createEnabled, setCreateEnabled] = useState(true);
  const [createSystemManager, setCreateSystemManager] = useState(false);
  const [createUserManager, setCreateUserManager] = useState(false);
  const [createRules, setCreateRules] = useState('');

  const [editing, setEditing] = useState<User | null>(null);
  const [editEnabled, setEditEnabled] = useState(true);
  const [editSystemManager, setEditSystemManager] = useState(false);
  const [editUserManager, setEditUserManager] = useState(false);
  const [editRules, setEditRules] = useState('');
  const [editOldPassword, setEditOldPassword] = useState('');
  const [editNewPassword, setEditNewPassword] = useState('');

  const [passwordFor, setPasswordFor] = useState<string | null>(null);
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');

  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const parseRules = (value: string) =>
    value
      .split(',')
      .map((r) => r.trim())
      .filter(Boolean);

  const isOwnAccount = (email: string) =>
    !!currentUser && currentUser.toLowerCase() === email.toLowerCase();

  const load = async (cursor?: string, append = false) => {
    if (append) {
      // A ref guard blocks a second click before React re-renders the button.
      if (loadingMoreRef.current) return;
      loadingMoreRef.current = true;
      setLoadingMore(true);
    }
    try {
      const data = await getUsers(cursor);
      setUsers((prev) => uniqueUsers(append ? [...prev, ...(data.users || [])] : data.users || []));
      setNextCursor(data.nextCursor || undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load users');
    } finally {
      if (append) {
        loadingMoreRef.current = false;
        setLoadingMore(false);
      }
      setLoading(false);
    }
  };

  useEffect(() => {
    // Keep the initial fetch behind a local async wrapper so the effect does
    // not synchronously update state; load only updates state after awaits.
    const initialize = async () => {
      await load();
    };
    void initialize();
  }, []);

  const refresh = async () => {
    setLoading(true);
    setError('');
    await load();
  };

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    setMessage('');
    try {
      await createUser({
        email: createEmail.trim(),
        password: createNoPassword ? '' : createPassword,
        enabled: createEnabled,
        systemManager: createSystemManager,
        userManager: createUserManager,
        rules: parseRules(createRules),
      });
      setShowCreate(false);
      setCreateEmail('');
      setCreatePassword('');
      setCreateNoPassword(false);
      setCreateEnabled(true);
      setCreateSystemManager(false);
      setCreateUserManager(false);
      setCreateRules('');
      setMessage('User created');
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create user');
    } finally {
      setBusy(false);
    }
  };

  const openEdit = (user: User) => {
    setEditing(user);
    setEditEnabled(user.enabled);
    setEditSystemManager(user.systemManager);
    setEditUserManager(user.userManager);
    setEditRules((user.rules || []).join(', '));
    setEditOldPassword('');
    setEditNewPassword('');
    setMessage('');
    setError('');
  };

  const onEdit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      const update: { enabled: boolean; systemManager: boolean; userManager: boolean; rules: string[]; oldPassword?: string; newPassword?: string } = {
        enabled: editEnabled,
        systemManager: editSystemManager,
        userManager: editUserManager,
        rules: parseRules(editRules),
      };
      if (editNewPassword.trim()) {
        if (editOldPassword.trim()) update.oldPassword = editOldPassword;
        update.newPassword = editNewPassword;
      }
      await updateUser(editing.email, update);
      setEditing(null);
      setMessage('User updated');
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update user');
    } finally {
      setBusy(false);
    }
  };

  const onDelete = async () => {
    if (!deleteTarget || isOwnAccount(deleteTarget)) return;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      await deleteUser(deleteTarget);
      setMessage('User deleted');
      setDeleteTarget(null);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to delete user');
      setDeleteTarget(null);
    } finally {
      setBusy(false);
    }
  };

  const onChangePassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!passwordFor) return;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      const body: { oldPassword?: string; newPassword: string } = { newPassword };
      if (oldPassword.trim()) body.oldPassword = oldPassword;
      await changeUserPassword(passwordFor, body);
      setPasswordFor(null);
      setOldPassword('');
      setNewPassword('');
      setMessage('Password changed');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to change password');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="users-page">
      <div className="email-list-header">
        <h2>User Management</h2>
        <button className="btn btn-primary" onClick={() => { setShowCreate((v) => !v); setCreateNoPassword(false); setMessage(''); setError(''); }}>
          {showCreate ? 'Close' : 'Create User'}
        </button>
      </div>

      {error && <div className="error-message" style={{ marginBottom: '1rem' }}>{error}</div>}
      {message && <div className="success-message" style={{ marginBottom: '1rem' }}>{message}</div>}

      {showCreate && (
        <form className="compose-card" style={{ marginBottom: '1.5rem', padding: '1rem' }} onSubmit={onCreate}>
          <h3 style={{ marginBottom: '1rem' }}>Create user</h3>
          <div className="form-group">
            <label htmlFor="create-email">Email</label>
            <input id="create-email" type="email" value={createEmail} onChange={(e) => setCreateEmail(e.target.value)} required disabled={busy} />
          </div>
          <div className="form-group">
            <label htmlFor="create-password">Password</label>
            <input
              id="create-password"
              type={createNoPassword ? 'text' : 'password'}
              value={createNoPassword ? 'cannot log in' : createPassword}
              onChange={(e) => setCreatePassword(e.target.value)}
              required={!createNoPassword}
              placeholder={createNoPassword ? 'cannot log in' : undefined}
              disabled={busy || createNoPassword}
            />
            {createNoPassword && <small style={{ color: 'var(--color-text-muted)' }}>cannot log in</small>}
          </div>
          <div className="form-group" style={{ flexDirection: 'row', gap: '1.5rem', alignItems: 'center' }}>
            <label>
              <input
                type="checkbox"
                checked={createNoPassword}
                onChange={(e) => {
                  setCreateNoPassword(e.target.checked);
                  if (e.target.checked) setCreatePassword('');
                }}
                disabled={busy}
              />
              No password
            </label>
            <label><input type="checkbox" checked={createEnabled} onChange={(e) => setCreateEnabled(e.target.checked)} disabled={busy} /> Enabled</label>
            <label><input type="checkbox" checked={createSystemManager} onChange={(e) => setCreateSystemManager(e.target.checked)} disabled={busy} /> System Manager</label>
            <label><input type="checkbox" checked={createUserManager} onChange={(e) => setCreateUserManager(e.target.checked)} disabled={busy} /> User Manager</label>
          </div>
          <div className="form-group">
            <label htmlFor="create-rules">Rules (comma-separated)</label>
            <input id="create-rules" type="text" value={createRules} onChange={(e) => setCreateRules(e.target.value)} placeholder="e.g. bounce, delete" disabled={busy} />
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
        </form>
      )}

      {editing && (
        <form className="compose-card" style={{ marginBottom: '1.5rem', padding: '1rem' }} onSubmit={onEdit}>
          <h3 style={{ marginBottom: '1rem' }}>Edit {editing.email}</h3>
          <div className="form-group" style={{ flexDirection: 'row', gap: '1.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
            <label><input type="checkbox" checked={editEnabled} onChange={(e) => setEditEnabled(e.target.checked)} disabled={busy} /> Enabled</label>
            <label><input type="checkbox" checked={editSystemManager} onChange={(e) => setEditSystemManager(e.target.checked)} disabled={busy} /> System Manager</label>
            <label><input type="checkbox" checked={editUserManager} onChange={(e) => setEditUserManager(e.target.checked)} disabled={busy} /> User Manager</label>
          </div>
          <div className="form-group">
            <label htmlFor="edit-rules">Rules (comma-separated)</label>
            <input id="edit-rules" type="text" value={editRules} onChange={(e) => setEditRules(e.target.value)} disabled={busy} />
          </div>
          <hr style={{ margin: '1rem 0', borderColor: 'var(--color-border)' }} />
          <div className="form-group">
            <label htmlFor="edit-old-password">Old password (required when changing your own password)</label>
            <input id="edit-old-password" type="password" value={editOldPassword} onChange={(e) => setEditOldPassword(e.target.value)} disabled={busy} />
          </div>
          <div className="form-group">
            <label htmlFor="edit-new-password">New password (leave blank to keep current)</label>
            <input id="edit-new-password" type="password" value={editNewPassword} onChange={(e) => setEditNewPassword(e.target.value)} disabled={busy} />
          </div>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <button className="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
            <button className="btn btn-secondary" type="button" onClick={() => setEditing(null)} disabled={busy}>Cancel</button>
          </div>
        </form>
      )}

      {passwordFor && (
        <form className="compose-card" style={{ marginBottom: '1.5rem', padding: '1rem' }} onSubmit={onChangePassword}>
          <h3 style={{ marginBottom: '1rem' }}>Change password for {passwordFor}</h3>
          <p style={{ color: 'var(--color-text-muted)', fontSize: '0.875rem', marginBottom: '1rem' }}>
            Old password is required when changing your own password; optional for system managers changing another user.
          </p>
          <div className="form-group">
            <label htmlFor="old-password">Old password (if required)</label>
            <input id="old-password" type="password" value={oldPassword} onChange={(e) => setOldPassword(e.target.value)} disabled={busy} />
          </div>
          <div className="form-group">
            <label htmlFor="new-password">New password</label>
            <input id="new-password" type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} required disabled={busy} />
          </div>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <button className="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Changing…' : 'Change password'}</button>
            <button className="btn btn-secondary" type="button" onClick={() => { setPasswordFor(null); setOldPassword(''); setNewPassword(''); }} disabled={busy}>Cancel</button>
          </div>
        </form>
      )}

      {loading ? (
        <div className="email-list-loading"><div className="spinner"></div></div>
      ) : users.length === 0 ? (
        <div className="empty-state"><p>No users found</p></div>
      ) : (
        <div className="compose-card" style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--color-border)' }}>
                <th style={{ padding: '0.75rem 1rem' }}>Email</th>
                <th style={{ padding: '0.75rem 1rem' }}>Enabled</th>
                <th style={{ padding: '0.75rem 1rem' }}>System Manager</th>
                <th style={{ padding: '0.75rem 1rem' }}>User Manager</th>
                <th style={{ padding: '0.75rem 1rem' }}>Rules</th>
                <th style={{ padding: '0.75rem 1rem' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.email} style={{ borderBottom: '1px solid var(--color-border)' }}>
                  <td style={{ padding: '0.75rem 1rem' }}>{u.email}</td>
                  <td style={{ padding: '0.75rem 1rem' }}>{u.enabled ? 'Yes' : 'No'}</td>
                  <td style={{ padding: '0.75rem 1rem' }}>{u.systemManager ? 'Yes' : 'No'}</td>
                  <td style={{ padding: '0.75rem 1rem' }}>{u.userManager ? 'Yes' : 'No'}</td>
                  <td style={{ padding: '0.75rem 1rem' }}>{(u.rules || []).join(', ') || '—'}</td>
                  <td style={{ padding: '0.75rem 1rem', whiteSpace: 'nowrap' }}>
                    <div style={{ display: 'flex', gap: '0.375rem', alignItems: 'center' }}>
                      <button className="btn btn-secondary" onClick={() => openEdit(u)} disabled={busy} style={{ padding: '0.375rem 0.75rem', fontSize: '0.8125rem' }}>Edit</button>
                      <button
                        className="btn btn-secondary"
                        onClick={() => setMasqueradeTarget(u.email)}
                        disabled={busy}
                        aria-label={`Masquerade as ${u.email}`}
                        title={`Masquerade as ${u.email}`}
                        style={{ padding: '0.375rem', fontSize: '0.8125rem', minWidth: '36px' }}
                      >
                        <MaskIcon />
                      </button>
                      <button
                        className="btn btn-danger"
                        onClick={() => { setDeleteTarget(u.email); setMessage(''); setError(''); }}
                        disabled={busy || isOwnAccount(u.email)}
                        title={isOwnAccount(u.email) ? 'You cannot delete your own account' : undefined}
                        style={{ padding: '0.375rem 0.75rem', fontSize: '0.8125rem' }}
                      >
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {nextCursor && (
            <div style={{ padding: '1rem' }}>
              <button className="btn btn-secondary" onClick={() => void load(nextCursor, true)} disabled={busy || loading || loadingMore}>
                Load more
              </button>
            </div>
          )}
        </div>
      )}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete user"
        message={`Delete user ${deleteTarget ?? ''}? This cannot be undone.`}
        busy={busy}
        onConfirm={onDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
