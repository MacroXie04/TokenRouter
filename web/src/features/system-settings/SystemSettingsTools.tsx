import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { testDeploymentConnection } from "../models";
import {
  clearAffinityCache,
  confirmPaymentCompliance,
  isAbortError,
  loadAffinityCacheStats,
  resetModelPricing,
  type AffinityCacheStats,
  type SystemOption,
} from './system-settings-api';

type Notice = { kind: 'success' | 'error'; text: string } | null;

function optionValue(options: readonly SystemOption[], key: string): string {
  return options.find((option) => option.key === key)?.value ?? '';
}

export function DeploymentConnectionPanel() {
  const { t } = useTranslation();
  const mounted = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);

  async function testConnection() {
    if (busy) return;
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setBusy(true);
    setNotice(null);
    try {
      await testDeploymentConnection(request.signal);
      if (mounted.current && !request.signal.aborted) {
        setNotice({ kind: 'success', text: t('io.net connection successful.') });
      }
    } catch (error) {
      if (!isAbortError(error) && mounted.current) {
        setNotice({ kind: 'error', text: t('Unable to connect to io.net with the stored API key.') });
      }
    } finally {
      if (mounted.current && !request.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby="deployment-connection-title">
      <h3 id="deployment-connection-title">{t('Connection check')}</h3>
      <p>{t('Tests the saved write-only API key against the deployment provider.')}</p>
      <button type="button" disabled={busy} onClick={() => void testConnection()}>
        {busy ? t('Testing…') : t('Test saved connection')}
      </button>
      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
    </section>
  );
}

export function PaymentCompliancePanel({ options, onChanged }: {
  options: readonly SystemOption[];
  onChanged: () => Promise<void> | void;
}) {
  const { t } = useTranslation();
  const controller = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const confirmed = optionValue(options, 'payment_setting.compliance_confirmed') === 'true'
    && optionValue(options, 'payment_setting.compliance_terms_version') === 'v1';
  const confirmedAt = Number(optionValue(options, 'payment_setting.compliance_confirmed_at'));
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);

  async function confirm() {
    if (confirmed || busy || !acknowledged) return;
    const request = new AbortController();
    controller.current = request;
    setBusy(true);
    setNotice(null);
    try {
      await confirmPaymentCompliance(request.signal);
      if (!mounted.current || request.signal.aborted) return;
      setNotice({ kind: 'success', text: t('Payment compliance confirmed.') });
      await onChanged();
    } catch (error) {
      if (!isAbortError(error) && mounted.current) setNotice({ kind: 'error', text: t('Request failed.') });
    } finally {
      if (mounted.current && !request.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby="payment-compliance-title">
      <h3 id="payment-compliance-title">{t('Payment compliance')}</h3>
      {confirmed ? (
        <p className="system-settings-notice success" role="status">
          {t('Current payment terms are confirmed.')}
          {Number.isSafeInteger(confirmedAt) && confirmedAt > 0 ? ` ${new Date(confirmedAt * 1_000).toLocaleString()}` : ''}
        </p>
      ) : (
        <>
          <p>{t('Confirmation is required before positive referral rewards can be enabled.')}</p>
          <label className="system-settings-checkbox">
            <input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />
            {t('I confirm that payment processing complies with the current v1 terms.')}
          </label>
          <button type="button" className="primary" disabled={busy || !acknowledged} onClick={() => void confirm()}>
            {busy ? t('Saving…') : t('Confirm payment compliance')}
          </button>
        </>
      )}
      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
    </section>
  );
}

export function PricingResetPanel({ onChanged }: { onChanged: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const mounted = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);

  async function reset() {
    if (busy) return;
    const request = new AbortController();
    controller.current = request;
    setBusy(true);
    setNotice(null);
    try {
      await resetModelPricing(request.signal);
      if (!mounted.current || request.signal.aborted) return;
      setConfirming(false);
      setNotice({ kind: 'success', text: t('Model pricing reset to built-in defaults.') });
      await onChanged();
    } catch (error) {
      if (!isAbortError(error) && mounted.current) setNotice({ kind: 'error', text: t('Request failed.') });
    } finally {
      if (mounted.current && !request.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby="pricing-reset-title">
      <h3 id="pricing-reset-title">{t('Pricing reset')}</h3>
      <p>{t('Restore the built-in ModelPrice and ModelRatio registries in one server-side operation.')}</p>
      <button type="button" className="danger" onClick={() => setConfirming(true)}>{t('Reset model pricing')}</button>
      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      {confirming && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby="pricing-confirm-title" aria-describedby="pricing-confirm-description">
            <h3 id="pricing-confirm-title">{t('Reset model pricing?')}</h3>
            <p id="pricing-confirm-description">{t('This replaces the current ModelPrice and ModelRatio values with built-in defaults. This action cannot be undone from the dashboard.')}</p>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setConfirming(false)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void reset()}>{busy ? t('Resetting…') : t('Reset')}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}

export function AffinityCachePanel() {
  const { t } = useTranslation();
  const mounted = useRef(true);
  const generation = useRef(0);
  const loadController = useRef<AbortController | null>(null);
  const mutationController = useRef<AbortController | null>(null);
  const [stats, setStats] = useState<AffinityCacheStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [confirming, setConfirming] = useState<string | 'all' | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      loadController.current?.abort();
      mutationController.current?.abort();
    };
  }, []);

  const load = useCallback(async () => {
    const current = ++generation.current;
    loadController.current?.abort();
    const request = new AbortController();
    loadController.current = request;
    setLoading(true);
    setError(false);
    try {
      const result = await loadAffinityCacheStats(request.signal);
      if (mounted.current && current === generation.current && !request.signal.aborted) setStats(result);
    } catch (caught) {
      if (mounted.current && current === generation.current && !isAbortError(caught)) setError(true);
    } finally {
      if (mounted.current && current === generation.current && !request.signal.aborted) setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  async function clear() {
    if (!confirming || busy) return;
    const selection = confirming;
    mutationController.current?.abort();
    const request = new AbortController();
    mutationController.current = request;
    setBusy(true);
    setNotice(null);
    try {
      const deleted = await clearAffinityCache(selection === 'all' ? { all: true } : { ruleName: selection }, request.signal);
      if (!mounted.current || request.signal.aborted) return;
      setConfirming(null);
      setNotice({ kind: 'success', text: `${t('Affinity cache entries deleted:')} ${new Intl.NumberFormat().format(deleted)}` });
      await load();
    } catch (caught) {
      if (!isAbortError(caught) && mounted.current) setNotice({ kind: 'error', text: t('Request failed.') });
    } finally {
      if (mounted.current && !request.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="system-settings-tool" aria-labelledby="affinity-cache-title">
      <header className="system-settings-tool-heading">
        <div><h3 id="affinity-cache-title">{t('Affinity cache')}</h3><p>{t('Live cache counts are loaded from the root-only affinity endpoint.')}</p></div>
        <button type="button" disabled={loading || busy} onClick={() => void load()}>{t('Refresh')}</button>
      </header>
      {loading ? <p role="status">{t('Loading…')}</p> : error || !stats ? (
        <div className="system-settings-inline-error" role="alert"><span>{t('Unable to load affinity cache statistics.')}</span><button type="button" onClick={() => void load()}>{t('Retry')}</button></div>
      ) : (
        <>
          <dl className="system-settings-stats">
            <div><dt>{t('Status')}</dt><dd>{stats.enabled ? t('Enabled') : t('Disabled')}</dd></div>
            <div><dt>{t('Entries')}</dt><dd>{new Intl.NumberFormat().format(stats.total)}</dd></div>
            <div><dt>{t('Capacity')}</dt><dd>{new Intl.NumberFormat().format(stats.cacheCapacity)}</dd></div>
            <div><dt>{t('Algorithm')}</dt><dd>{stats.cacheAlgorithm}</dd></div>
          </dl>
          <div className="system-settings-row-actions">
            {Object.keys(stats.byRuleName).map((rule) => (
              <button type="button" key={rule} disabled={busy || stats.byRuleName[rule] === 0} onClick={() => setConfirming(rule)}>
                {t('Clear')} {rule} ({stats.byRuleName[rule]})
              </button>
            ))}
            <button type="button" className="danger" disabled={busy || stats.total === 0} onClick={() => setConfirming('all')}>{t('Clear all affinity cache')}</button>
          </div>
        </>
      )}
      {notice && <p className={`system-settings-notice ${notice.kind}`} role={notice.kind === 'error' ? 'alert' : 'status'}>{notice.text}</p>}
      {confirming && (
        <div className="system-settings-overlay" role="presentation">
          <section className="system-settings-dialog" role="alertdialog" aria-modal="true" aria-labelledby="affinity-clear-title" aria-describedby="affinity-clear-description">
            <h3 id="affinity-clear-title">{t('Clear affinity cache?')}</h3>
            <p id="affinity-clear-description">{confirming === 'all' ? t('Every sticky-channel cache entry will be deleted.') : `${t('Cache entries for this rule will be deleted:')} ${confirming}`}</p>
            <footer className="system-settings-dialog-actions">
              <button type="button" autoFocus disabled={busy} onClick={() => setConfirming(null)}>{t('Cancel')}</button>
              <button type="button" className="danger" disabled={busy} onClick={() => void clear()}>{busy ? t('Clearing…') : t('Clear cache')}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}
