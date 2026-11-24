import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  deleteSupportReceiverRule,
  getSupportReceiverIdentities,
  getSupportReceiverRules,
  saveSupportReceiverRules,
  searchSupportDeliveryUsers,
} from '../api.ts';
import type {
  HandlerRule,
  ReceiverDomainRules,
  ReceiverIdentity,
} from '../api/generated/schemas/support/index.ts';
import ConfirmDialog from './ConfirmDialog.tsx';

const FALLBACK_IDENTITY = '_';
const DELIVER_TO_USER: HandlerRule = 'DeliverToUser';
const DELIVER_TO_PREFIX = 'DeliverTo:';

// The generated HandlerRule is a string, not an enum: DeliverTo rules carry a
// saved user address. The fixed choices live here; the parameterised one uses
// an address input beside the dropdown.
const RULE_OPTIONS: HandlerRule[] = [
  DELIVER_TO_USER,
  DELIVER_TO_PREFIX,
  'DeliverToMailIdentity',
  'Bounce',
  'Drop',
];

function deliverToAddress(rule: HandlerRule): string | null {
  return rule.startsWith(DELIVER_TO_PREFIX) ? rule.slice(DELIVER_TO_PREFIX.length) : null;
}

function validRule(rule: HandlerRule): boolean {
  const address = deliverToAddress(rule);
  if (address === null) {
    return RULE_OPTIONS.includes(rule) && rule !== DELIVER_TO_PREFIX;
  }
  return /^[^@\s]+@[^@\s]+$/.test(address);
}

function sortRules(rules: ReceiverDomainRules[]): ReceiverDomainRules[] {
  return [...rules].sort((a, b) => {
    if (a.domain === FALLBACK_IDENTITY) return -1;
    if (b.domain === FALLBACK_IDENTITY) return 1;
    return a.domain.localeCompare(b.domain);
  });
}

// Delivery targets use the same search semantics as the masquerade picker:
// wait for three characters, debounce, and show the user manager's
// case-insensitive substring matches. Selecting a suggestion writes the full
// address back into the rule.
interface DeliveryTargetInputProps {
  value: string;
  ariaLabel: string;
  onChange: (value: string) => void;
}

function DeliveryTargetInput({ value, ariaLabel, onChange }: DeliveryTargetInputProps) {
  const [results, setResults] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);
  const [error, setError] = useState('');
  const debounceTimer = useRef<number | null>(null);
  const cache = useRef(new Map<string, string[]>());
  const requestSeq = useRef(0);

  useEffect(() => () => {
    if (debounceTimer.current !== null) {
      window.clearTimeout(debounceTimer.current);
    }
  }, []);

  const updateValue = (next: string) => {
    onChange(next);
    if (debounceTimer.current !== null) {
      window.clearTimeout(debounceTimer.current);
      debounceTimer.current = null;
    }
    const q = next.trim();
    const seq = ++requestSeq.current;

    if (q.length < 3) {
      setLoading(false);
      setResults([]);
      setHasSearched(false);
      setError('');
      return;
    }

    const cached = cache.current.get(q.toLowerCase());
    if (cached) {
      setLoading(false);
      setResults(cached);
      setHasSearched(true);
      setError('');
      return;
    }

    setLoading(true);
    setError('');
    debounceTimer.current = window.setTimeout(() => {
      debounceTimer.current = null;
      void (async () => {
        try {
          const data = await searchSupportDeliveryUsers(q);
          const users = Array.isArray(data.users) ? data.users.slice(0, 10) : [];
          cache.current.set(q.toLowerCase(), users);
          if (seq === requestSeq.current) {
            setResults(users);
            setHasSearched(true);
            setError('');
          }
        } catch (e) {
          if (seq === requestSeq.current) {
            setResults([]);
            setHasSearched(true);
            setError(e instanceof Error ? e.message : 'Failed to search users');
          }
        } finally {
          if (seq === requestSeq.current) setLoading(false);
        }
      })();
    }, 100);
  };

  const showEmpty = !loading && !error && hasSearched && results.length === 0;

  return (
    <div className="receiver-delivery-input">
      <input
        aria-label={ariaLabel}
        className="receiver-rule-address"
        value={value}
        placeholder="user@example.com"
        onChange={(event) => updateValue(event.target.value)}
      />
      {loading && <span className="spinner small" aria-label="Searching users" />}
      {showEmpty && <div className="receiver-user-empty">No matching users</div>}
      {error && <div className="receiver-user-error">{error}</div>}
      {!loading && results.length > 0 && (
        <div className="receiver-user-suggestions" role="listbox" aria-label={`Matching users for ${ariaLabel}`}>
          {results.map((user) => (
            <button
              key={user}
              type="button"
              className="receiver-user-suggestion"
              onClick={() => {
                onChange(user);
                setResults([]);
                setHasSearched(false);
              }}
            >
              {user}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export default function ReceiverSupport() {
  const navigate = useNavigate();
  const [identities, setIdentities] = useState<ReceiverIdentity[]>([]);
  const [rules, setRules] = useState<ReceiverDomainRules[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');

  // Load the two lists independently: the identities call depends on SES and
  // can fail (for example missing ses:ListIdentities), but that must never
  // discard a successful rules response.
  const load = async () => {
    const failures: string[] = [];
    try {
      const rulesData = await getSupportReceiverRules();
      setRules(sortRules(rulesData.rules || []));
    } catch (e) {
      failures.push(e instanceof Error ? e.message : 'Failed to load receiver rules');
    }
    try {
      const identityData = await getSupportReceiverIdentities();
      setIdentities(identityData.identities || []);
    } catch (e) {
      failures.push(e instanceof Error ? e.message : 'Failed to load receiving mail identities');
    }
    setError(failures.join('; '));
    setLoading(false);
  };

  useEffect(() => {
    // Keep the initial fetch behind a local async wrapper so the effect does
    // not synchronously update state; load only updates state after awaits.
    const initialize = async () => {
      await load();
    };
    void initialize();
  }, []);

  const onSave = async () => {
    if (rules.some((entry) => entry.rules.some((rule) => !validRule(rule)))) {
      setError('Enter a valid user address for each DeliverTo rule, for example DeliverTo:catchall@example.com.');
      return;
    }
    const targets = new Set(
      rules.flatMap((entry) => entry.rules.map(deliverToAddress).filter((address): address is string => address !== null)),
    );
    setSaving(true);
    setError('');
    setMessage('');
    try {
      // The backend also checks every target on save. Doing the same search
      // here gives immediate feedback using the masquerade picker's semantics.
      for (const target of targets) {
        if (target.trim().length < 3) continue;
        const data = await searchSupportDeliveryUsers(target);
        const known = (data.users || []).some((user) => user.trim().toLowerCase() === target.trim().toLowerCase());
        if (!known) {
          throw new Error(`Delivery user "${target}" was not found. Choose a matching user.`);
        }
      }
      const saved = await saveSupportReceiverRules(rules);
      setRules(sortRules(saved.rules || []));
      const identityData = await getSupportReceiverIdentities();
      setIdentities(identityData.identities || []);
      setMessage('Receiver rules saved.');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save receiver rules');
    } finally {
      setSaving(false);
    }
  };

  // The fallback definition can never be removed: every recipient without its
  // own entry relies on it. The backend rejects the same request.
  const onRemove = async () => {
    const identity = removing;
    if (!identity || identity === FALLBACK_IDENTITY) return;

    // An identity added with "+" but not yet saved has no store entry, so
    // DELETE would report 404. Drop that draft locally instead.
    const savedOnServer = identities.some((item) => (
      item.identity.toLowerCase() === identity.toLowerCase() && item.hasRules
    ));
    if (!savedOnServer) {
      setRules((current) => current.filter((rule) => rule.domain.toLowerCase() !== identity.toLowerCase()));
      setMessage(`Removed unsaved rules for ${identity}.`);
      setRemoving(null);
      return;
    }

    setDeleting(true);
    setError('');
    setMessage('');
    try {
      const remaining = await deleteSupportReceiverRule(identity);
      setRules(sortRules(remaining.rules || []));
      setIdentities((current) => current.map((item) => (
        item.identity.toLowerCase() === identity.toLowerCase()
          ? { ...item, hasRules: false }
          : item
      )));
      setMessage(`Rules for ${identity} removed. It now uses the fallback rules.`);
      setRemoving(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to remove receiver rules');
    } finally {
      setDeleting(false);
    }
  };

  // A plus next to a receiving mail identity adds one ordered rules list,
  // seeded from the fallback, so it can be edited before pressing OK.
  const addDomain = (domain: string) => {
    setError('');
    setMessage('');
    setRules((current) => {
      if (current.some((rule) => rule.domain.toLowerCase() === domain.toLowerCase())) {
        return current;
      }
      const fallbackRules = current.find((rule) => rule.domain === FALLBACK_IDENTITY)?.rules;
      const rules = fallbackRules?.length
        ? [...fallbackRules]
        : [DELIVER_TO_USER, 'Bounce'];
      return sortRules([...current, { domain, rules }]);
    });
  };

  const editRules = (domain: string, edit: (rules: HandlerRule[]) => HandlerRule[]) => {
    setError('');
    setMessage('');
    setRules((current) => sortRules(current.map((rule) => (
      rule.domain === domain ? { ...rule, rules: edit(rule.rules) } : rule
    ))));
  };

  const moveRule = (domain: string, index: number, delta: number) => {
    editRules(domain, (rules) => {
      const target = index + delta;
      if (target < 0 || target >= rules.length) return rules;
      const reordered = [...rules];
      [reordered[index], reordered[target]] = [reordered[target], reordered[index]];
      return reordered;
    });
  };

  const configured = (identity: ReceiverIdentity) => identity.hasRules || rules.some(
    (rule) => rule.domain.toLowerCase() === identity.identity.toLowerCase(),
  );

  return (
    <div className="support-page">
      <div className="email-list-header">
        <h2>Receiver rules</h2>
        <div className="receiver-actions">
          <button className="btn btn-secondary" onClick={() => navigate('/support')} disabled={saving || deleting}>
            Cancel
          </button>
          <button
            className="btn btn-secondary"
            onClick={() => { setLoading(true); setError(''); setMessage(''); void load(); }}
            disabled={loading || saving || deleting}
          >
            {loading ? 'Loading…' : 'Reload'}
          </button>
          <button className="btn btn-primary" onClick={() => void onSave()} disabled={loading || saving || deleting}>
            {saving ? 'Saving…' : 'OK'}
          </button>
        </div>
      </div>

      {error && <div className="error-message">{error}</div>}
      {message && <div className="success-message">{message}</div>}

      <section className="settings-section">
        <h3>Receiving mail identities</h3>
        {loading ? (
          <div className="email-list-loading"><div className="spinner"></div></div>
        ) : identities.length === 0 ? (
          <div className="empty-state"><p>No receiving mail identities found.</p></div>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--color-border)' }}>
                  <th style={{ padding: '0.75rem 1rem' }}>Receiving identity</th>
                  <th style={{ padding: '0.75rem 1rem' }}>Rules</th>
                </tr>
              </thead>
              <tbody>
                {identities.map((identity) => (
                  <tr key={identity.identity} style={{ borderBottom: '1px solid var(--color-border)' }}>
                    <td style={{ padding: '0.75rem 1rem' }}><code>{identity.identity}</code></td>
                    <td style={{ padding: '0.75rem 1rem' }}>
                      {configured(identity) ? (
                        <span className="receiver-tick" title="Rules configured">✓</span>
                      ) : (
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => addDomain(identity.identity)}
                          title={`Add rules for ${identity.identity}`}
                        >
                          +
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="settings-section">
        <h3>Configured rules</h3>
        {loading ? (
          <div className="email-list-loading"><div className="spinner"></div></div>
        ) : rules.length === 0 ? (
          <div className="empty-state"><p>No receiver rules configured.</p></div>
        ) : (
          rules.map((rule) => {
            const isFallback = rule.domain === FALLBACK_IDENTITY;
            return (
              <div className="receiver-domain" key={rule.domain}>
                <div className="receiver-domain-header">
                  <span>
                    Receiving identity: <code>{rule.domain}</code>
                    {isFallback && <span className="receiver-fallback-label">fallback</span>}
                  </span>
                  <button
                    className="btn btn-danger btn-small"
                    onClick={() => setRemoving(rule.domain)}
                    disabled={isFallback || deleting || saving}
                    title={isFallback ? 'The fallback rules cannot be removed' : `Remove rules for ${rule.domain}`}
                  >
                    Remove
                  </button>
                </div>
                <div className="receiver-rule-list">
                  {rule.rules.map((ruleName, index) => (
                    <div className="receiver-rule-row" key={index}>
                      <select
                        aria-label={`Rule ${index + 1} for ${rule.domain}`}
                        value={deliverToAddress(ruleName) === null ? ruleName : DELIVER_TO_PREFIX}
                        onChange={(event) => editRules(rule.domain, (current) => {
                          const updated = [...current];
                          updated[index] = event.target.value as HandlerRule;
                          return updated;
                        })}
                      >
                        {RULE_OPTIONS.map((option) => (
                          <option key={option} value={option}>
                            {option === DELIVER_TO_PREFIX ? 'DeliverTo:<user address>' : option}
                          </option>
                        ))}
                      </select>
                      {deliverToAddress(ruleName) !== null && (
                        <DeliveryTargetInput
                          ariaLabel={`Delivery address for rule ${index + 1} for ${rule.domain}`}
                          value={deliverToAddress(ruleName) ?? ''}
                          onChange={(address) => editRules(rule.domain, (current) => {
                            const updated = [...current];
                            updated[index] = `${DELIVER_TO_PREFIX}${address}` as HandlerRule;
                            return updated;
                          })}
                        />
                      )}
                      <button
                        className="btn btn-secondary btn-small"
                        onClick={() => moveRule(rule.domain, index, -1)}
                        disabled={index === 0}
                        title="Move rule up"
                      >
                        ↑
                      </button>
                      <button
                        className="btn btn-secondary btn-small"
                        onClick={() => moveRule(rule.domain, index, 1)}
                        disabled={index === rule.rules.length - 1}
                        title="Move rule down"
                      >
                        ↓
                      </button>
                      <button
                        className="btn btn-danger btn-small"
                        onClick={() => editRules(rule.domain, (current) => current.filter((_, i) => i !== index))}
                        disabled={rule.rules.length === 1}
                        title="Remove rule"
                      >
                        ×
                      </button>
                    </div>
                  ))}
                </div>
                <button
                  className="btn btn-secondary btn-small"
                  onClick={() => editRules(rule.domain, (current) => [...current, DELIVER_TO_USER])}
                >
                  + Add rule
                </button>
              </div>
            );
          })
        )}
      </section>

      <ConfirmDialog
        open={removing !== null}
        title="Remove receiving identity"
        message={`Remove the rules for "${removing}"? Mail for this identity will use the fallback rules until you add them again.`}
        confirmLabel="Remove"
        busy={deleting}
        onConfirm={() => void onRemove()}
        onCancel={() => setRemoving(null)}
      />
    </div>
  );
}
