import type { WalletViewController } from './useWalletController';

type WalletRedemptionPanelProps = Pick<WalletViewController,
  'busy' | 'doRedeem' | 'info' | 'infoLoading'
  | 'redeemCode' | 'redemptionReady' | 'setRedeemCode' | 't'
>;

export function WalletRedemptionPanel({
  busy, doRedeem, info, infoLoading,
  redeemCode, redemptionReady, setRedeemCode, t,
}: WalletRedemptionPanelProps) {
  return (
    <>
      <section className="card">
        <h2>{t('Redeem a code')}</h2>
        {infoLoading ? (
          <p className="muted" role="status">{t('Loading payment options…')}</p>
        ) : !redemptionReady ? (
          <p className="muted">{t('Code redemption is currently unavailable.')}</p>
        ) : (
          <form onSubmit={doRedeem}>
            <label>
              {t('Redemption code')}
              <input
                value={redeemCode}
                onChange={(event) => setRedeemCode(event.target.value)}
                maxLength={128}
                autoComplete="off"
                required
              />
            </label>
            <button type="submit" disabled={Boolean(busy) || redeemCode.trim() === ''}>
              {busy === 'redeem' ? t('Redeeming…') : t('Redeem code')}
            </button>
          </form>
        )}
        {redemptionReady && info?.topUpLink && (
          <p><a href={info.topUpLink} target="_blank" rel="noopener noreferrer">{t('Get a redemption code')}</a></p>
        )}
      </section>
    </>
  );
}
