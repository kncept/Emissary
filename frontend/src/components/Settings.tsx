import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import type { AuthState } from '../types';

interface SettingsProps {
  auth: AuthState;
  onLogout: () => void | Promise<void>;
}

const MASQUERADE_AS_KEY = 'masquerade_as';

export default function Settings({ auth, onLogout }: SettingsProps) {
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [masqueradeAs, setMasqueradeAs] = useState<string | null>(null);
  const [loggingOut, setLoggingOut] = useState(false);

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