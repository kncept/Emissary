import { Link, useLocation } from 'react-router-dom';
import { useState } from 'react';
import MasqueradeModal from './MasqueradeModal';
import MaskIcon from './MaskIcon';
import { MASQUERADE_AS_KEY } from '../api/jmap-client';
import { requestEmailRefresh } from '../api/email-checks.ts';
import {
  clearMasquerade as stopMasquerade,
  setMasqueradeTarget as startMasquerade,
} from '../masquerade.ts';

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

  const selectMasqueradeTarget = (value: string) => {
    startMasquerade(value);
    setMasqueradeAs(value);
    setMasqueradeOpen(false);
  };

  const stopMasquerading = () => {
    stopMasquerade();
    setMasqueradeAs(null);
  };

  const handleNavClick = (path: string) => {
    setMobileMenuOpen(false);
    // A click on Inbox is an explicit refresh, even when already on "/".
    // From another route the inbox has not mounted yet, so there is no
    // subscriber and its normal mount load handles the refresh instead.
    if (path === '/') requestEmailRefresh();
  };

  return (
    <div className="app-layout">
      <header className="app-header">
        <div className="header-left">
          <h1 className="app-title">Emissary</h1>
          <nav
            className={`main-nav${mobileMenuOpen ? ' open' : ''}`}
            aria-label="Main navigation"
          >
            <ul>
              {navItems.map((item) => (
                <li key={item.path}>
                  <Link
                    to={item.path}
                    className={location.pathname === item.path ? 'active' : ''}
                    onClick={() => handleNavClick(item.path)}
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
                onClick={stopMasquerading}
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
              <MaskIcon />
              Masquerade
            </button>
          )}
          <button
            type="button"
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
        onSelect={selectMasqueradeTarget}
      />
    </div>
  );
}