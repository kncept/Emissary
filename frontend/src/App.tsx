import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { useEffect, useState } from 'react';
import type { AuthState } from './types.ts';
import { checkAuth, logout as apiLogout } from './api.ts';
import Login from './components/Login';
import EmailList from './components/EmailList';
import EmailView from './components/EmailView';
import Compose from './components/Compose';
import Users from './components/Users';
import Settings from './components/Settings';
import Support from './components/Support';
import Layout from './components/Layout';
import './App.css';

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
                canMasquerade={auth.systemManager}
              >
                <Routes>
                  <Route path="/" element={<EmailList />} />
                  <Route path="/email/:id" element={<EmailView />} />
                  <Route path="/compose" element={<Compose systemManager={auth.systemManager} currentUser={auth.username} />} />
                  <Route path="/compose/:id" element={<Compose systemManager={auth.systemManager} currentUser={auth.username} />} />
                  <Route path="/settings" element={<Settings auth={auth} />} />
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
                    path="/users"
                    element={
                      auth.emissaryUserManagement ? (
                        <Users />
                      ) : (
                        <Navigate to="/" replace />
                      )
                    }
                  />
                </Routes>
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