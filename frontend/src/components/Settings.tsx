import { useState, useEffect } from 'react';
import type { AuthState } from '../types';

interface SettingsProps {
  auth: AuthState;
}

export default function Settings({ auth }: SettingsProps) {
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    // Reset messages when auth changes
    setError(null);
    setSuccess(null);
  }, [auth]);

  return (
    <div className="settings-page">
      <h2>Settings</h2>

      <section className="settings-section">
        <h3>Account Information</h3>
        <div className="settings-field">
          <label>Mailbox ID</label>
          <div className="settings-value">{auth.email || auth.username || 'Unknown'}</div>
        </div>

        {auth.admin && (
          <div className="settings-field">
            <label>Admin Status</label>
            <div className="settings-value admin-badge">
              <span className="badge admin">Admin</span>
            </div>
          </div>
        )}

        {auth.admin && auth.emissaryUserManagement && (
          <div className="settings-field">
            <label>Emissary User Management</label>
            <div className="settings-value admin-badge">
              <span className="badge management">Enabled</span>
            </div>
          </div>
        )}
      </section>

      <section className="settings-section">
        <h3>Session</h3>
        <div className="settings-field">
          <label>Status</label>
          <div className="settings-value">
            <span className="badge active">Active</span>
          </div>
        </div>
        <button className="btn btn-danger" onClick={() => window.location.href = '/login'}>
          Logout
        </button>
      </section>

      {error && <div className="error-message">{error}</div>}
      {success && <div className="success-message">{success}</div>}
    </div>
  );
}