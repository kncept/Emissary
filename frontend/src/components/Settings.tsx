import { useState, useEffect, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { changeOwnPassword, isPasswordChangeSupported } from '../api.ts';
import type { AuthState } from '../types';

interface SettingsProps {
  auth: AuthState;
  onLogout: () => void | Promise<void>;
}

const MASQUERADE_AS_KEY = 'masquerade_as';

export default function Settings({ auth, onLogout }: SettingsProps) {
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [masqueradeAs] = useState<string | null>(() => {
    try {
      return window.localStorage.getItem(MASQUERADE_AS_KEY);
    } catch {
      return null;
    }
  });
  const [loggingOut, setLoggingOut] = useState(false);
  const [changingPassword, setChangingPassword] = useState(false);
  const [passwordBusy, setPasswordBusy] = useState(false);
  const [passwordChanged, setPasswordChanged] = useState(false);
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [passwordChangeSupported, setPasswordChangeSupported] = useState(false);

  useEffect(() => {
    // The API helper caches this promise in memory, so this runs at most once
    // per page load even if Settings is remounted.
    let active = true;
    isPasswordChangeSupported()
      .then((supported) => {
        if (active) setPasswordChangeSupported(supported);
      })
      .catch(() => {
        if (active) setPasswordChangeSupported(false);
      });
    return () => {
      active = false;
    };
  }, []);

  // Reset messages and close the form when the session changes. Doing this
  // during render avoids a synchronous state update inside an effect.
  const sessionKey = [
    auth.authenticated,
    auth.username,
    auth.email,
    auth.systemManager,
    auth.emissaryUserManagement,
  ].join('|');
  const [previousSessionKey, setPreviousSessionKey] = useState(sessionKey);
  if (previousSessionKey !== sessionKey) {
    setPreviousSessionKey(sessionKey);
    setError(null);
    setSuccess(null);
    setChangingPassword(false);
    setPasswordChanged(false);
    setCurrentPassword('');
    setNewPassword('');
    setConfirmPassword('');
  }

  const handleLogout = async () => {
    setError(null);
    setLoggingOut(true);
    try {
      await onLogout();
    } catch {
      // The app keeps the user signed in when the server logout fails, so
      // keep this screen usable and let them try again.
      setError('Failed to log out. Please try again.');
      setLoggingOut(false);
    }
  };

  const resetPasswordForm = () => {
    setCurrentPassword('');
    setNewPassword('');
    setConfirmPassword('');
  };

  const handlePasswordChange = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSuccess(null);
    setPasswordChanged(false);
    if (!currentPassword) {
      setError('Current password is required.');
      return;
    }
    if (!newPassword) {
      setError('New password is required.');
      return;
    }
    if (newPassword !== confirmPassword) {
      setError('New passwords do not match.');
      return;
    }

    setPasswordBusy(true);
    try {
      await changeOwnPassword({ oldPassword: currentPassword, newPassword });
      resetPasswordForm();
      setChangingPassword(false);
      setPasswordChanged(true);
      setSuccess('Password changed. Sign in again with your new password.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to change password.');
    } finally {
      setPasswordBusy(false);
    }
  };

  return (
    <div className="settings-page">
      <h2>Settings</h2>

      <section className="settings-section">
        <h3>Account Information</h3>
        <div className="settings-field">
          <label>User ID (email address)</label>
          <div className="settings-value">{auth.email || auth.username || 'Unknown'}</div>
        </div>

        {masqueradeAs && (
          <div className="settings-field">
            <label>Masquerading as</label>
            <div className="settings-value">{masqueradeAs}</div>
          </div>
        )}

        {auth.systemManager && (
          <div className="settings-field">
            <label>System Manager Status</label>
            <div className="settings-value admin-badge">
              <span className="badge admin">System Manager</span>
            </div>
          </div>
        )}

        {auth.emissaryUserManagement && (
          <div className="settings-field">
            <label>Emissary User Management</label>
            <div className="settings-value admin-badge">
              <span className="badge management">Enabled</span>
            </div>
          </div>
        )}
      </section>

      {passwordChangeSupported && (
        <section className="settings-section">
          <h3>Password</h3>
          {!changingPassword && (
            <>
              <p style={{ color: 'var(--color-text-muted)', marginBottom: '1rem' }}>
                Change the password for {auth.email || auth.username || 'your account'}.
              </p>
              <button
                className="btn btn-secondary"
                onClick={() => {
                  setError(null);
                  setSuccess(null);
                  setPasswordChanged(false);
                  setChangingPassword(true);
                }}
              >
                Change password
              </button>
              {passwordChanged && (
                <button
                  className="btn btn-primary"
                  style={{ marginLeft: '0.5rem' }}
                  onClick={handleLogout}
                  disabled={loggingOut}
                >
                  {loggingOut ? 'Signing out…' : 'Sign in again'}
                </button>
              )}
            </>
          )}

          {changingPassword && (
            <form onSubmit={handlePasswordChange}>
              <div className="form-group">
                <label htmlFor="settings-current-password">Current password</label>
                <input
                  id="settings-current-password"
                  type="password"
                  autoComplete="current-password"
                  value={currentPassword}
                  onChange={(e) => setCurrentPassword(e.target.value)}
                  disabled={passwordBusy}
                  required
                />
              </div>
              <div className="form-group">
                <label htmlFor="settings-new-password">New password</label>
                <input
                  id="settings-new-password"
                  type="password"
                  autoComplete="new-password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  disabled={passwordBusy}
                  required
                />
              </div>
              <div className="form-group">
                <label htmlFor="settings-confirm-password">Confirm new password</label>
                <input
                  id="settings-confirm-password"
                  type="password"
                  autoComplete="new-password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  disabled={passwordBusy}
                  required
                />
              </div>
              <div style={{ display: 'flex', gap: '0.5rem' }}>
                <button className="btn btn-primary" type="submit" disabled={passwordBusy}>
                  {passwordBusy ? 'Changing…' : 'Change password'}
                </button>
                <button
                  className="btn btn-secondary"
                  type="button"
                  disabled={passwordBusy}
                  onClick={() => {
                    resetPasswordForm();
                    setChangingPassword(false);
                  }}
                >
                  Cancel
                </button>
              </div>
            </form>
          )}
        </section>
      )}

      <section className="settings-section">
        <h3>Session</h3>
        <div className="settings-field">
          <label>Status</label>
          <div className="settings-value">
            <span className="badge active">Active</span>
          </div>
        </div>
        <button
          className="btn btn-danger"
          onClick={handleLogout}
          disabled={loggingOut}
        >
          {loggingOut ? 'Logging out...' : 'Logout'}
        </button>
      </section>

      {auth.systemManager && (
        <section className="settings-section">
          <h3>Support</h3>
          <p style={{ color: 'var(--color-text-muted)', marginBottom: '1rem' }}>
            Inspect runtime configuration, list unprocessed inbound messages and reprocess them from staging.
          </p>
          <Link className="btn btn-secondary" to="/support">
            Open support tools
          </Link>
        </section>
      )}

      {error && <div className="error-message">{error}</div>}
      {success && <div className="success-message">{success}</div>}
    </div>
  );
}