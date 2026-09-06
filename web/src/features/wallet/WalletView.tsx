import { TurnstileWidget, } from "../security";
import { useWalletController } from './useWalletController';
import { number } from './wallet-presentation';
import type { WalletViewProps } from './wallet-view-props';
import { WalletAffiliatePanel } from './WalletAffiliatePanel';
import { WalletCreditPanel } from './WalletCreditPanel';
import { WalletHistoryPanel } from './WalletHistoryPanel';
import { WalletRedemptionPanel } from './WalletRedemptionPanel';
import { WalletSubscriptionsPanel } from './WalletSubscriptionsPanel';
export type { WalletViewProps } from './wallet-view-props';

export function WalletView(props: WalletViewProps) {
  const controller = useWalletController(props);
  const {
    busy, checkedIn, doCheckIn, i18n,
    notice, onNavigate, setNotice, setTurnstileOpen,
    setTurnstileWidgetKey, signOut, t, turnstileConfig,
    turnstileOpen, turnstileWidgetKey, walletQuotaText, walletUser,
  } = controller;

  return (
    <main className="app wallet-app" aria-label={t('Wallet')}>
      {turnstileOpen && (
        <div className="modal-overlay">
          <section className="modal" role="dialog" aria-modal="true" aria-label={t('Security check')}>
            <h2>{t('Security check')}</h2>
            <p className="muted">{t('Complete the security check to continue.')}</p>
            <TurnstileWidget
              key={turnstileWidgetKey}
              className="turnstile-widget"
              siteKey={turnstileConfig.siteKey}
              label={t('Human verification')}
              onVerify={(challenge) => { void doCheckIn(challenge); }}
              onExpire={() => setTurnstileWidgetKey((value) => value + 1)}
              onError={() => {
                setTurnstileOpen(false);
                setNotice({ kind: 'error', text: t('Human verification is unavailable.') });
              }}
            />
            <div className="modal-actions">
              <button
                type="button"
                className="link"
                disabled={busy === 'checkin'}
                onClick={() => {
                  setTurnstileOpen(false);
                  setTurnstileWidgetKey((value) => value + 1);
                }}
              >
                {t('Close')}
              </button>
            </div>
          </section>
        </div>
      )}
      <header className="header row">
        <div>
          <h1>{t('Wallet')}</h1>
          <p className="tagline">{t('Manage balance, payments, rewards, and subscriptions.')}</p>
        </div>
        <nav className="wallet-header-actions" aria-label={t('Wallet navigation')}>
          <button type="button" className="link" onClick={() => onNavigate('/dashboard')}>{t('Dashboard')}</button>
          <button type="button" className="link" onClick={signOut} disabled={Boolean(busy)}>{t('Sign out')}</button>
        </nav>
      </header>

      {notice && <p className={notice.kind === 'error' ? 'error' : 'success'} role="status" aria-live="polite">{notice.text}</p>}

      <section className="wallet-stat-grid" aria-label={t('Wallet balance')}>
        <article className="card wallet-stat"><span>{t('Remaining')}</span><strong>{walletQuotaText(walletUser.quota)}</strong></article>
        <article className="card wallet-stat"><span>{t('Used')}</span><strong>{walletQuotaText(walletUser.used_quota)}</strong></article>
        <article className="card wallet-stat"><span>{t('Requests')}</span><strong>{number(walletUser.request_count, i18n.language)}</strong></article>
      </section>

      <section className="card wallet-checkin">
        <div>
          <h2>{t('Daily check-in')}</h2>
          <p className="muted">{checkedIn === true ? t('You have checked in today.') : t('Claim today’s available quota reward.')}</p>
        </div>
        <button type="button" onClick={() => { void doCheckIn(); }} disabled={Boolean(busy) || checkedIn === true}>
          {busy === 'checkin' ? t('Checking in…') : checkedIn === true ? t('Checked in') : t('Check in today')}
        </button>
      </section>

      <div className="wallet-columns">
        <WalletCreditPanel {...controller} />

        <WalletRedemptionPanel {...controller} />
      </div>

      <WalletHistoryPanel {...controller} />

      <div className="wallet-columns">
        <WalletAffiliatePanel {...controller} />

        <WalletSubscriptionsPanel {...controller} />
      </div>

      <footer className="footer">TokenRouter — {t('independent AI API gateway.')}</footer>
    </main>
  );
}
