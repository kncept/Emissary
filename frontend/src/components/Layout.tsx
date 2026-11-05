import { Outlet, Link, useLocation } from 'react-router-dom';
import { useState } from 'react';

interface LayoutProps {
  children?: React.ReactNode;
  onLogout: () => void;
}

export default function Layout({ onLogout }: LayoutProps) {
  const location = useLocation();
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  const navItems = [
    { path: '/', label: 'Inbox', icon: '📥' },
    { path: '/compose', label: 'Compose', icon: '✏️' },
  ];

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
      <main className="app-main">
        <Outlet />
      </main>
      {mobileMenuOpen && (
        <div className="mobile-overlay" onClick={() => setMobileMenuOpen(false)} />
      )}
    </div>
  );
}