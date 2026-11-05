import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { useEffect, useState } from 'react';
import { checkAuth, logout as apiLogout } from './api';
import Login from './components/Login';
import EmailList from './components/EmailList';
import EmailView from './components/EmailView';
import Compose from './components/Compose';
import Layout from './components/Layout';
import './App.css';

function App() {
  const [authenticated, setAuthenticated] = useState(false);
  const [checkingAuth, setCheckingAuth] = useState(true);

  useEffect(() => {
    checkAuth().then(({ authenticated }) => {
      setAuthenticated(authenticated);
      setCheckingAuth(false);
    }).catch(() => {
      setAuthenticated(false);
      setCheckingAuth(false);
    });
  }, []);

  const handleAuthenticated = () => {
    setAuthenticated(true);
  };

  const handleLogout = async () => {
    await apiLogout();
    setAuthenticated(false);
  };

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
                  <Route path="/" element={<EmailList />} />
                  <Route path="/email/:id" element={<EmailView />} />
                  <Route path="/compose" element={<Compose />} />
                  <Route path="/compose/:id" element={<Compose />} />
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