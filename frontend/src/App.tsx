import { useEffect, useState } from 'react'
import Login from './Login'
import Mailbox from './Mailbox'
import { getToken, setToken } from './api'
import './App.css'

function App() {
  const [token, setTokenState] = useState<string | null>(() => getToken())

  useEffect(() => {
    function handleUnauthorized() {
      setToken(null)
      setTokenState(null)
    }
    window.addEventListener('emissary:unauthorized', handleUnauthorized)
    return () => window.removeEventListener('emissary:unauthorized', handleUnauthorized)
  }, [])

  function handleLogin() {
    setTokenState(getToken())
  }

  function handleLogout() {
    setToken(null)
    setTokenState(null)
  }

  return token ? <Mailbox onLogout={handleLogout} /> : <Login onLogin={handleLogin} />
}

export default App
