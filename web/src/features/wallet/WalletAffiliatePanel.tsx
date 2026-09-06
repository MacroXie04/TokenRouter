import type { WalletViewController } from './useWalletController';
import { formatQuotaForDisplay, number, quotaDisplayUnit } from './wallet-presentation';

type WalletAffiliatePanelProps = Pick<WalletViewController,
  'affiliate' | 'affiliateAmount' | 'affiliateError' | 'affiliateLoading'
  | 'affiliateTransferReady' | 'busy' | 'complianceReady' | 'complianceUnavailable'
  | 'copiedKey' | 'copyValue' | 'doAffiliateTransfer' | 'i18n'
  | 'info' | 'loadAffiliate' | 'referralLink' | 'setAffiliateAmount'
  | 't'
>;

export function WalletAffiliatePanel({
  affiliate, affiliateAmount, affiliateError, affiliateLoading,
  affiliateTransferReady, busy, complianceReady, complianceUnavailable,
  copiedKey, copyValue, doAffiliateTransfer, i18n,
  info, loadAffiliate, referralLink, setAffiliateAmount,
  t,
}: WalletAffiliatePanelProps) {
  return (
    <>
      <section className="card">
        <h2>{t('Affiliate rewards')}</h2>
        {affiliateLoading && <p className="muted" role="status">{t('Loading affiliate rewards…')}</p>}
        {affiliateError && (
          <div className="error-panel" role="alert"><p>{t('Unable to load affiliate rewards.')}</p><button type="button" className="link" onClick={() => loadAffiliate()}>{t('Try again')}</button></div>
        )}
        {!affiliateLoading && !affiliateError && affiliate && (
          <>
            <dl className="kv">
              <dt>{t('Referral code')}</dt><dd><code>{affiliate.code}</code></dd>
              <dt>{t('Referred users')}</dt><dd>{number(affiliate.count, i18n.language)}</dd>
              <dt>{t('Available rewards')}</dt><dd>{info
                ? formatQuotaForDisplay(affiliate.availableQuota, info, i18n.language)
                : number(affiliate.availableQuota, i18n.language)}</dd>
              <dt>{t('Lifetime rewards')}</dt><dd>{info
                ? formatQuotaForDisplay(affiliate.lifetimeQuota, info, i18n.language)
                : number(affiliate.lifetimeQuota, i18n.language)}</dd>
            </dl>
            <label>
              {t('Referral link')}
              <span className="wallet-copy-value wallet-referral-link">
                <input value={referralLink} readOnly aria-label={t('Referral link')} />
                <button
                  type="button"
                  className="button"
                  aria-label={t('Copy referral link')}
                  onClick={() => { void copyValue(referralLink, 'referral', t('Referral link copied.')); }}
                >
                  {copiedKey === 'referral' ? t('Copied') : t('Copy')}
                </button>
              </span>
            </label>
            {complianceReady ? (
              <form onSubmit={doAffiliateTransfer}>
                <label>
                  {info
                    ? t('Amount to transfer ({{unit}})', { unit: quotaDisplayUnit(info) })
                    : t('Transfer amount')}
                  <input
                    type="text"
                    inputMode={info?.quotaDisplayType === 'tokens' ? 'numeric' : 'decimal'}
                    pattern={info?.quotaDisplayType === 'tokens' ? '[1-9][0-9]*' : '(?:0|[1-9][0-9]*)(?:\\.[0-9]{1,6})?'}
                    maxLength={32}
                    value={affiliateAmount}
                    onChange={(event) => setAffiliateAmount(event.target.value)}
                    required
                  />
                </label>
                {info && (
                  <div className="wallet-transfer-summary">
                    <span className="muted">{t('Minimum transfer')}: {formatQuotaForDisplay(info.quotaPerUnit, info, i18n.language)}</span>
                    <span className="muted">{t('Available')}: {formatQuotaForDisplay(affiliate.availableQuota, info, i18n.language)}</span>
                  </div>
                )}
                {info && affiliate.availableQuota < info.quotaPerUnit && (
                  <p className="muted">{t('At least {{minimum}} is required before rewards can be transferred.', {
                    minimum: formatQuotaForDisplay(info.quotaPerUnit, info, i18n.language),
                  })}</p>
                )}
                <button type="submit" disabled={Boolean(busy) || !affiliateTransferReady}>{busy === 'affiliate' ? t('Transferring…') : t('Transfer to wallet')}</button>
              </form>
            ) : <p className="muted">{complianceUnavailable}</p>}
          </>
        )}
      </section>
    </>
  );
}
