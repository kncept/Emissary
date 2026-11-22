import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react';
import { autocompleteUsers } from '../api';

interface MasqueradeModalProps {
  open: boolean;
  onClose: () => void;
  onSelect: (email: string) => void;
}

type CacheEntry = { q: string; users: string[] };

export default function MasqueradeModal({ open, onClose, onSelect }: MasqueradeModalProps) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [hasSearched, setHasSearched] = useState(false);

  const debounceTimer = useRef<number | null>(null);
  const inFlight = useRef(false);
  const pendingQuery = useRef<string | null>(null);
  const latestQuery = useRef('');
  const cache = useRef<CacheEntry[]>([]);
  const requestSeq = useRef(0);
  const closeButton = useRef<HTMLButtonElement>(null);

  const clearDebounce = () => {
    if (debounceTimer.current !== null) {
      window.clearTimeout(debounceTimer.current);
      debounceTimer.current = null;
    }
  };

  const getCached = (q: string): string[] | undefined => {
    const entry = cache.current.find((e) => e.q === q);
    return entry?.users;
  };

  const setCached = (q: string, users: string[]) => {
    const next = cache.current.filter((e) => e.q !== q);
    next.push({ q, users });
    if (next.length > 3) {
      next.shift();
    }
    cache.current = next;
  };

  const showForQuery = (q: string, users: string[]) => {
    if (latestQuery.current !== q) return;
    setResults(users);
    setHasSearched(true);
    setError('');
  };

  const runSearch = async (q: string) => {
    // Only search when >= 3 characters
    if (q.trim().length < 3) {
      return;
    }

    // Serve from cache if available (counts as latest valid result)
    const cached = getCached(q);
    if (cached) {
      if (latestQuery.current === q) {
        setLoading(false);
        showForQuery(q, cached);
      }
      // After serving cache, if a newer query is pending we still need to honor it
      const pending = pendingQuery.current;
      pendingQuery.current = null;
      if (pending && pending !== q && latestQuery.current === pending) {
        void runSearch(pending);
      }
      return;
    }

    if (inFlight.current) {
      pendingQuery.current = q;
      return;
    }

    inFlight.current = true;
    setLoading(true);
    setError('');
    const seq = ++requestSeq.current;

    try {
      const data = await autocompleteUsers(q);
      const users = Array.isArray(data.users) ? data.users.slice(0, 10) : [];
      setCached(q, users);
      // Only show if still the latest query
      if (latestQuery.current === q && seq === requestSeq.current) {
        showForQuery(q, users);
      }
    } catch (e) {
      if (latestQuery.current === q) {
        setError(e instanceof Error ? e.message : 'Failed to search users');
        setResults([]);
        setHasSearched(true);
      }
    } finally {
      inFlight.current = false;
      // Spinner should reflect whether we're still working toward latest
      const pending = pendingQuery.current;
      pendingQuery.current = null;
      if (pending && latestQuery.current === pending && pending.trim().length >= 3) {
        // Continue with latest pending query (only one in-flight at a time)
        void runSearch(pending);
      } else if (!inFlight.current) {
        // No more work for latest; hide spinner if nothing pending for latest
        if (latestQuery.current.trim().length < 3 || getCached(latestQuery.current) || latestQuery.current === q) {
          setLoading(false);
        } else {
          setLoading(false);
        }
      }
    }
  };

  const scheduleSearch = (value: string) => {
    clearDebounce();
    const q = value;
    latestQuery.current = q;

    if (q.trim().length < 3) {
      pendingQuery.current = null;
      setLoading(false);
      setResults([]);
      setHasSearched(false);
      setError('');
      return;
    }

    debounceTimer.current = window.setTimeout(() => {
      debounceTimer.current = null;
      void runSearch(latestQuery.current);
    }, 100);
  };

  useEffect(() => {
    if (!open) {
      clearDebounce();
      setQuery('');
      setResults([]);
      setLoading(false);
      setError('');
      setHasSearched(false);
      pendingQuery.current = null;
      latestQuery.current = '';
      inFlight.current = false;
      return;
    }
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    closeButton.current?.focus();
    return () => {
      clearDebounce();
      previousFocus?.focus();
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [open, onClose]);

  if (!open) return null;

  const trimmed = query.trim();
  const showEmpty = !loading && !error && hasSearched && trimmed.length >= 3 && results.length === 0;

  const handleOverlayClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget) onClose();
  };

  return (
    <div
      className="modal-overlay"
      role="presentation"
      onClick={handleOverlayClick}
    >
      <div className="modal-card" role="dialog" aria-modal="true" aria-label="Masquerade">
        <div className="modal-header">
          <h3>Masquerade</h3>
          <button
            ref={closeButton}
            type="button"
            className="icon-btn"
            onClick={onClose}
            aria-label="Close"
          >
            ✕
          </button>
        </div>
        <div className="modal-body">
          <label htmlFor="masquerade-email" className="modal-label">
            User ID (email address)
          </label>
          <div className="masquerade-input-row">
            <input
              id="masquerade-email"
              type="text"
              value={query}
              onChange={(e) => {
                const v = e.target.value;
                setQuery(v);
                scheduleSearch(v);
              }}
              placeholder="Type at least 3 characters"
              autoComplete="off"
            />
            {loading && <span className="spinner small" aria-label="Searching"></span>}
          </div>

          {error && <div className="error-message" style={{ marginTop: '0.75rem' }}>{error}</div>}

          {trimmed.length >= 3 && !error && (results.length > 0 || showEmpty) && (
            <ul className="masquerade-results" role="listbox" aria-label="Matching users">
              {results.map((email) => (
                <li key={email}>
                  <button
                    type="button"
                    className="masquerade-result-item"
                    onClick={() => onSelect(email)}
                  >
                    {email}
                  </button>
                </li>
              ))}
              {showEmpty && <li className="masquerade-empty">No matches</li>}
            </ul>
          )}
        </div>
        <div className="modal-footer">
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => onSelect(query)}
          >
            OK
          </button>
        </div>
      </div>
    </div>
  );
}
