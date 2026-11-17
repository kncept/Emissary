import { useEffect, useState } from 'react';
import {
  getSupportBucketRoot,
  getSupportConfiguration,
  getSupportConfigurations,
  processSupportEmail,
} from '../api.ts';
import type {
  BucketRootResponse,
  ConfigurationDetail,
  ConfigurationSummary,
} from '../api/generated/schemas/support/index.ts';

function formatSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}

export default function Support() {
  const [configurations, setConfigurations] = useState<ConfigurationSummary[]>([]);
  const [selected, setSelected] = useState<ConfigurationDetail | null>(null);
  const [bucket, setBucket] = useState<BucketRootResponse | null>(null);
  const [loadingConfigurations, setLoadingConfigurations] = useState(true);
  const [loadingBucket, setLoadingBucket] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');

  const loadConfigurations = async () => {
    try {
      setLoadingConfigurations(true);
      setError('');
      const data = await getSupportConfigurations();
      setConfigurations(data.configurations || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load configurations');
    } finally {
      setLoadingConfigurations(false);
    }
  };

  const loadBucket = async () => {
    try {
      setLoadingBucket(true);
      setError('');
      setBucket(await getSupportBucketRoot());
    } catch (e) {
      setBucket(null);
      setError(e instanceof Error ? e.message : 'Failed to list the bucket root');
    } finally {
      setLoadingBucket(false);
    }
  };

  useEffect(() => {
    void loadConfigurations();
    void loadBucket();
  }, []);

  const openConfiguration = async (name: string) => {
    try {
      setError('');
      setMessage('');
      setSelected(await getSupportConfiguration(name));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load configuration');
    }
  };

  const onProcess = async (id: string) => {
    setBusyId(id);
    setError('');
    setMessage('');
    try {
      const result = await processSupportEmail(id);
      setMessage(result.message || `Processed ${result.id}`);
      await loadBucket();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to process email');
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="support-page">
      <div className="email-list-header">
        <h2>Support</h2>
        <button className="btn btn-secondary" onClick={() => { void loadConfigurations(); void loadBucket(); }}>
          Refresh
        </button>
      </div>

      {error && <div className="error-message">{error}</div>}
      {message && <div className="success-message">{message}</div>}

      <section className="settings-section">
        <h3>Configuration</h3>
        {loadingConfigurations ? (
          <div className="email-list-loading"><div className="spinner"></div></div>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--color-border)' }}>
                  <th style={{ padding: '0.75rem 1rem' }}>Section</th>
                  <th style={{ padding: '0.75rem 1rem' }}>Implementation</th>
                  <th style={{ padding: '0.75rem 1rem' }}>Description</th>
                  <th style={{ padding: '0.75rem 1rem' }}></th>
                </tr>
              </thead>
              <tbody>
                {configurations.map((configuration) => (
                  <tr key={configuration.name} style={{ borderBottom: '1px solid var(--color-border)' }}>
                    <td style={{ padding: '0.75rem 1rem' }}><code>{configuration.name}</code></td>
                    <td style={{ padding: '0.75rem 1rem' }}><span className="badge active">{configuration.implementation}</span></td>
                    <td style={{ padding: '0.75rem 1rem' }}>{configuration.description || '—'}</td>
                    <td style={{ padding: '0.75rem 1rem' }}>
                      <button className="btn btn-secondary" onClick={() => void openConfiguration(configuration.name)}>
                        View
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {selected && (
          <div style={{ marginTop: '1.5rem' }}>
            <div className="email-list-header" style={{ marginBottom: '0.75rem' }}>
              <h3 style={{ margin: 0 }}>{selected.name} details</h3>
              <button className="btn btn-secondary" onClick={() => setSelected(null)}>Close</button>
            </div>
            <p style={{ color: 'var(--color-text-muted)', marginBottom: '0.75rem' }}>
              {selected.description} · implementation <code>{selected.implementation}</code>
            </p>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <tbody>
                {Object.entries(selected.settings || {}).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => (
                  <tr key={key} style={{ borderBottom: '1px solid var(--color-border)' }}>
                    <th style={{ padding: '0.5rem 1rem', textAlign: 'left', width: '40%' }}>{key}</th>
                    <td style={{ padding: '0.5rem 1rem' }}>{value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {Object.keys(selected.settings || {}).length === 0 && (
              <div className="empty-state"><p>No settings reported.</p></div>
            )}
          </div>
        )}
      </section>

      <section className="settings-section">
        <div className="email-list-header" style={{ marginBottom: '0.75rem' }}>
          <h3 style={{ margin: 0 }}>Bucket root</h3>
          <button className="btn btn-secondary" onClick={() => void loadBucket()} disabled={loadingBucket}>
            {loadingBucket ? 'Loading…' : 'Reload'}
          </button>
        </div>
        <p style={{ color: 'var(--color-text-muted)', marginBottom: '0.75rem' }}>
          Unprocessed staging messages are listed here. Processing delivers them to user mailboxes and deletes the staging object.
        </p>

        {loadingBucket ? (
          <div className="email-list-loading"><div className="spinner"></div></div>
        ) : !bucket ? (
          <div className="empty-state"><p>Bucket contents are unavailable.</p></div>
        ) : (
          <>
            <div className="settings-field">
              <label>Bucket</label>
              <div className="settings-value"><code>{bucket.bucket}</code></div>
            </div>
            {bucket.prefixes.length > 0 && (
              <div className="settings-field">
                <label>Prefixes</label>
                <div className="settings-value">{bucket.prefixes.join(', ')}</div>
              </div>
            )}
            {bucket.objects.length === 0 ? (
              <div className="empty-state"><p>No staging messages.</p></div>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                  <thead>
                    <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--color-border)' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Key</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Size</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Last modified</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {bucket.objects.map((object) => (
                      <tr key={object.key} style={{ borderBottom: '1px solid var(--color-border)' }}>
                        <td style={{ padding: '0.75rem 1rem' }}><code>{object.key}</code></td>
                        <td style={{ padding: '0.75rem 1rem' }}>{formatSize(object.size)}</td>
                        <td style={{ padding: '0.75rem 1rem' }}>{new Date(object.lastModified).toLocaleString()}</td>
                        <td style={{ padding: '0.75rem 1rem' }}>
                          <button className="btn btn-primary" onClick={() => void onProcess(object.key)} disabled={busyId !== null}>
                            {busyId === object.key ? 'Processing…' : 'Process'}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  );
}
