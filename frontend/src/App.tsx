import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { useEffect, useState } from 'react';
import type { AuthState } from './types';
import { checkAuth, logout as apiLogout } from './api';
import Login from './components/Login';
import EmailList from './components/EmailList';
import EmailView from './components/EmailView';
import Compose from './components/Compose';
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
              <Layout onLogout={handleLogout}>
                <Routes>
                  <Route path="/" element={<EmailList admin={auth.admin} />} />
                  <Route path="/email/:id" element={<EmailView />} />
                  <Route path="/compose" element={<Compose admin={auth.admin} currentUser={auth.username} />} />
                  <Route path="/compose/:id" element={<Compose admin={auth.admin} currentUser={auth.username} />} />
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