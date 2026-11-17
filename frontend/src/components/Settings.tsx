import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import type { AuthState } from '../types';

interface SettingsProps {
  auth: AuthState;
}

const MASQUERADE_AS_KEY = 'masquerade_as';

export default function Settings({ auth }: SettingsProps) {
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [masqueradeAs, setMasqueradeAs] = useState<string | null>(null);

  useEffect(() => {
    // Reset messages when auth changes
    setError(null);
    setSuccess(null);
  }, [auth]);

  useEffect(() => {
    try {
      setMasqueradeAs(window.localStorage.getItem(MASQUERADE_AS_KEY));
    } catch {
      setMasqueradeAs(null);
    }
  }, []);

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

      {auth.admin && (
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