import { Link, useLocation } from 'react-router-dom';
import { useState } from 'react';
import MasqueradeModal from './MasqueradeModal';
import { MASQUERADE_AS_KEY } from '../api/jmap-client';

interface LayoutProps {
  children?: React.ReactNode;
  onLogout: () => void;
  canManageUsers?: boolean;
  canMasquerade?: boolean;
}

export default function Layout({
  children,
  onLogout,
  canManageUsers = false,
  canMasquerade = false,
}: LayoutProps) {
  const location = useLocation();
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [masqueradeOpen, setMasqueradeOpen] = useState(false);
  const [masqueradeAs, setMasqueradeAs] = useState<string | null>(() => {
    try {
      return window.localStorage.getItem(MASQUERADE_AS_KEY);
    } catch {
      return null;
    }
  });

  const navItems = [
    { path: '/', label: 'Inbox', icon: '📥' },
    { path: '/compose', label: 'Compose', icon: '✏️' },
    { path: '/settings', label: 'Settings', icon: '⚙️' },
    ...(canManageUsers ? [{ path: '/users', label: 'Users', icon: '👥' }] : []),
  ];

  const setMasqueradeTarget = (value: string) => {
    try {
      window.localStorage.setItem(MASQUERADE_AS_KEY, value);
    } catch {
      // ignore storage errors
    }
    setMasqueradeAs(value);
    setMasqueradeOpen(false);
    window.location.assign('/');
  };

  const clearMasquerade = () => {
    try {
      window.localStorage.removeItem(MASQUERADE_AS_KEY);
    } catch {
      // ignore storage errors
    }
    setMasqueradeAs(null);
    window.location.assign('/');
  };

  return (
    <div className="app-layout">
      <header className="app-header">
        <div className="header-left">
          <h1 className="app-title">Emissary</h1>
          <nav className="main-nav" aria-label="Main navigation">
            <ul>
              {navItems.map((item) => (
                <li key={item.path}>
                  <Link
                    to={item.path}
                    className={location.pathname === item.path ? 'active' : ''}
                  >
                    <span className="nav-icon">{item.icon}</span>
                    <span className="nav-label">{item.label}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </nav>
        </div>
        <div className="header-right">
          {canMasquerade && masqueradeAs && (
            <div className="masquerade-indicator" title={masqueradeAs}>
              <span className="masquerade-indicator-label">{masqueradeAs}</span>
              <button
                type="button"
                className="masquerade-indicator-clear"
                onClick={clearMasquerade}
                aria-label="Stop masquerading"
                title="Stop masquerading"
              >
                ✕
              </button>
            </div>
          )}
          {canMasquerade && (
            <button
              className="masquerade-btn"
              onClick={() => setMasqueradeOpen(true)}
              type="button"
            >
              Masquerade
            </button>
          )}
          <button
            className="mobile-menu-btn"
            onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
            aria-expanded={mobileMenuOpen}
            aria-label="Toggle menu"
          >
            {mobileMenuOpen ? '✕' : '☰'}
          </button>
          <button className="logout-btn" onClick={onLogout}>
            Logout
          </button>
        </div>
      </header>
      <main className="app-main">{children}</main>
      {mobileMenuOpen && (
        <div className="mobile-overlay" onClick={() => setMobileMenuOpen(false)} />
      )}
      <MasqueradeModal
        open={masqueradeOpen}
        onClose={() => setMasqueradeOpen(false)}
        onSelect={setMasqueradeTarget}
      />
    </div>
  );
}