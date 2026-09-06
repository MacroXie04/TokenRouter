import { CheckoutControl } from './CheckoutControl';
import type { WalletViewController } from './useWalletController';
import { formatQuotaForDisplay, number } from './wallet-presentation';

type WalletCreditPanelProps = Pick<WalletViewController,
  'amount' | 'busy' | 'buyCreemProduct' | 'checkout'
  | 'complianceReady' | 'complianceUnavailable' | 'enteredPaymentAmount' | 'i18n'
  | 'info' | 'infoError' | 'infoLoading' | 'loadInfo'
  | 'options' | 'preview' | 'previewSignature' | 'reviewPayment'
  | 'selectedOption' | 'selectedPayment' | 'selectedPaymentBelowMinimum' | 'setAmount'
  | 'setCheckout' | 'setPreview' | 'setSelectedPayment' | 'showHistory'
  | 'startSelectedCheckout' | 't'
>;

export function WalletCreditPanel({
  amount, busy, buyCreemProduct, checkout,
  complianceReady, complianceUnavailable, enteredPaymentAmount, i18n,
  info, infoError, infoLoading, loadInfo,
  options, preview, previewSignature, reviewPayment,
  selectedOption, selectedPayment, selectedPaymentBelowMinimum, setAmount,
  setCheckout, setPreview, setSelectedPayment, showHistory,
  startSelectedCheckout, t,
}: WalletCreditPanelProps) {
  return (
    <>
      <section className="card">
        <div className="wallet-section-heading">
          <h2>{t('Add wallet credit')}</h2>
          <button type="button" className="link" onClick={showHistory}>{t('Payment history')}</button>
        </div>
        {infoLoading && <p className="muted" role="status">{t('Loading payment options…')}</p>}
        {infoError && (
          <div className="error-panel" role="alert">
            <p>{t('Unable to load payment options.')}</p>
            <button type="button" className="link" onClick={() => loadInfo()}>{t('Try again')}</button>
          </div>
        )}
        {!infoLoading && !infoError && !complianceReady && (
          <p className="muted">{complianceUnavailable}</p>
        )}
        {complianceReady && info && options.length === 0 && info.creemProducts.length === 0 && (
          <p className="muted">{t('No online payment methods are configured.')}</p>
        )}
        {!infoLoading && !infoError && options.length > 0 && (
          <form onSubmit={reviewPayment}>
            <fieldset className="wallet-payment-options">
              <legend>{t('Payment method')}</legend>
              {options.map((option) => {
                const belowMinimum = enteredPaymentAmount === null || enteredPaymentAmount < option.minimum;
                const minimumText = number(option.minimum, i18n.language);
                return (
                  <label
                    key={option.id}
                    data-selected={selectedPayment === option.id}
                    data-disabled={belowMinimum}
                    title={belowMinimum ? t('Requires at least {{amount}}', { amount: minimumText }) : undefined}
                  >
                    <input
                      type="radio"
                      name="wallet-payment"
                      value={option.id}
                      checked={selectedPayment === option.id}
                      disabled={Boolean(busy) || belowMinimum}
                      aria-label={belowMinimum
                        ? `${option.label}. ${t('Requires at least {{amount}}', { amount: minimumText })}`
                        : option.label}
                      onChange={() => { setSelectedPayment(option.id); setPreview(null); setCheckout(null); }}
                    />
                    <span>{option.label}</span>
                    <small>{belowMinimum
                      ? t('Requires at least {{amount}}', { amount: minimumText })
                      : t('Minimum {{amount}}', { amount: minimumText })}</small>
                  </label>
                );
              })}
            </fieldset>
            {info && info.presets.length > 0 && (
              <div className="wallet-presets" aria-label={t('Preset amounts')}>
                {info.presets.map((preset) => (
                  <button
                    type="button"
                    className="button"
                    key={preset}
                    onClick={() => { setAmount(String(preset)); setPreview(null); setCheckout(null); }}
                  >
                    {number(preset, i18n.language)}
                    {info.discounts[preset] !== undefined && info.discounts[preset] !== 1
                      ? ` × ${info.discounts[preset]}`
                      : ''}
                  </button>
                ))}
              </div>
            )}
            <label>
              {t('Top-up amount')}
              <input
                type="text"
                inputMode="numeric"
                pattern="[1-9][0-9]*"
                maxLength={16}
                value={amount}
                onChange={(event) => { setAmount(event.target.value); setPreview(null); setCheckout(null); }}
                required
              />
            </label>
            {selectedOption && selectedPaymentBelowMinimum && (
              <div className="wallet-minimum-help" role="status">
                <span>{t('Requires at least {{amount}}', { amount: number(selectedOption.minimum, i18n.language) })}</span>
                <button
                  type="button"
                  className="link"
                  disabled={Boolean(busy)}
                  onClick={() => { setAmount(String(selectedOption.minimum)); setPreview(null); setCheckout(null); }}
                >
                  {t('Use provider minimum')}
                </button>
              </div>
            )}
            <button type="submit" disabled={Boolean(busy) || selectedPaymentBelowMinimum}>{busy === 'preview' ? t('Calculating…') : t('Review payment')}</button>
          </form>
        )}
        {complianceReady && preview?.signature === previewSignature && selectedOption && (
          <div className="wallet-review" aria-live="polite">
            <p>{t('Payable amount')}: <strong>{preview.payable}</strong></p>
            <button type="button" onClick={startSelectedCheckout} disabled={Boolean(busy)}>
              {busy === 'checkout' ? t('Creating checkout…') : t('Create checkout')}
            </button>
          </div>
        )}
        {complianceReady && checkout && (
          <div className="wallet-checkout" role="status">
            <strong>{t('Checkout ready')}</strong>
            {checkout.orderId && <span className="muted">{t('Order {{id}}', { id: checkout.orderId })}</span>}
            <CheckoutControl checkout={checkout} label={t('Open secure checkout')} />
          </div>
        )}
        {complianceReady && info?.creemEnabled && info.creemProducts.length > 0 && (
          <div className="wallet-products">
            <h3>{t('Fixed-price products')}</h3>
            {info.creemProducts.map((product) => (
              <article key={product.productId}>
                <div>
                  <strong>{product.name}</strong>
                  <span className="muted">{formatQuotaForDisplay(product.quota, info, i18n.language)} · {product.price.toFixed(2)} {product.currency}</span>
                </div>
                <button type="button" className="link" onClick={() => buyCreemProduct(product.productId, product.name)} disabled={Boolean(busy)}>
                  {t('Buy')}
                </button>
              </article>
            ))}
          </div>
        )}
      </section>
    </>
  );
}
