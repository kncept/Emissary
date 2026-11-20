import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom';
import { useEffect, useState, type ReactNode } from 'react';
import type { AuthState } from './types.ts';
import { checkAuth, logout as apiLogout } from './api.ts';
import Login from './components/Login';
import EmailList from './components/EmailList';
import EmailView from './components/EmailView';
import Compose from './components/Compose';
import Users from './components/Users';
import Settings from './components/Settings';
import Support from './components/Support';
import ReceiverSupport from './components/ReceiverSupport';
import Layout from './components/Layout';
import ErrorBoundary from './components/ErrorBoundary';
import './App.css';

// Remount the boundary on navigation so a failure on one screen never follows
// the user to the next one.
function RouteBoundary({ children }: { children: ReactNode }) {
  const location = useLocation();
  return <ErrorBoundary key={location.pathname}>{children}</ErrorBoundary>;
}

function App() {
  const [auth, setAuth] = useState<AuthState>({ authenticated: false });
  const [checkingAuth, setCheckingAuth] = useState(true);

  useEffect(() => {
    checkAuth().then((state) => {
      setAuth(state);
      setCheckingAuth(false);
    }).catch(() => {
      setAuth({ authenticated: false });
      setCheckingAuth(false);
    });
  }, []);

  const handleAuthenticated = (state: AuthState) => {
    setAuth({ ...state, authenticated: true });
  };

  const handleLogout = async () => {
    await apiLogout();
    setAuth({ authenticated: false });
  };

  const authenticated = auth.authenticated;

  if (checkingAuth) {
    return (
      <div className="loading-screen">
        <div className="spinner"></div>
        <p>Loading...</p>
      </div>
    );
  }

  return (
    <BrowserRouter>
      <Routes>
        <Route
          path="/login"
          element={
            authenticated ? <Navigate to="/" replace /> : <Login onAuthenticated={handleAuthenticated} />
          }
        />
        <Route
          path="/*"
          element={
            authenticated ? (
              <Layout
                onLogout={handleLogout}
                canManageUsers={auth.emissaryUserManagement}
                canMasquerade={auth.emissaryUserManagement}
              >
                <RouteBoundary>
                  <Routes>
                    <Route path="/" element={<EmailList />} />
                    <Route path="/email/:id" element={<EmailView />} />
                    <Route path="/compose" element={<Compose currentUser={auth.username} />} />
                    <Route path="/compose/:id" element={<Compose mode="reply" currentUser={auth.username} />} />
                    <Route path="/compose/forward/:id" element={<Compose mode="forward" currentUser={auth.username} />} />
                    <Route path="/settings" element={<Settings auth={auth} onLogout={handleLogout} />} />
                    <Route
                      path="/support"
                      element={
                        auth.systemManager ? (
                          <Support />
                        ) : (
                          <Navigate to="/" replace />
                        )
                      }
                    />
                    <Route
                      path="/support/receiver"
                      element={
                        auth.systemManager ? (
                          <ReceiverSupport />
                        ) : (
                          <Navigate to="/" replace />
                        )
                      }
                    />
                    <Route
                      path="/users"
                      element={
                        auth.emissaryUserManagement ? (
                          <Users currentUser={auth.username} />
                        ) : (
                          <Navigate to="/" replace />
                        )
                      }
                    />
                  </Routes>
                </RouteBoundary>
              </Layout>
            ) : (
              <Navigate to="/login" replace />
            )
          }
        />
      </Routes>
    </BrowserRouter>
  );
}

export default App;