import { CheckoutControl } from './CheckoutControl';
import { SubscriptionRecordItem } from './SubscriptionRecordItem';
import type { WalletViewController } from './useWalletController';
import {
  type BillingPreference
} from './wallet-api';
import { durationUnitKeys, formatQuotaForDisplay, number, resetPeriodKeys } from './wallet-presentation';

type WalletSubscriptionsPanelProps = Pick<WalletViewController,
  'busy' | 'buySubscription' | 'changeBillingPreference' | 'complianceReady'
  | 'complianceUnavailable' | 'effectiveBillingPreference' | 'historicalSubscriptions' | 'i18n'
  | 'info' | 'loadSubscriptions' | 'planPurchaseState' | 'plans'
  | 'plansById' | 'startSubscriptionCheckout' | 'subscriptionCheckout' | 'subscriptions'
  | 'subscriptionsError' | 'subscriptionsLoading' | 't' | 'walletUser'
>;

export function WalletSubscriptionsPanel({
  busy, buySubscription, changeBillingPreference, complianceReady,
  complianceUnavailable, effectiveBillingPreference, historicalSubscriptions, i18n,
  info, loadSubscriptions, planPurchaseState, plans,
  plansById, startSubscriptionCheckout, subscriptionCheckout, subscriptions,
  subscriptionsError, subscriptionsLoading, t, walletUser,
}: WalletSubscriptionsPanelProps) {
  return (
    <>
      <section className="card">
        <h2>{t('Subscriptions')}</h2>
        {subscriptionsLoading && <p className="muted" role="status">{t('Loading subscriptions…')}</p>}
        {subscriptionsError && (
          <div className="error-panel" role="alert"><p>{t('Unable to load subscriptions.')}</p><button type="button" className="link" onClick={() => loadSubscriptions()}>{t('Try again')}</button></div>
        )}
        {!subscriptionsLoading && !subscriptionsError && subscriptions && (
          <>
            <label>
              {t('Billing preference')}
              <select value={effectiveBillingPreference ?? 'wallet_first'} disabled={Boolean(busy)} onChange={(event) => changeBillingPreference(event.target.value as BillingPreference)}>
                <option value="subscription_first" disabled={subscriptions.active.length === 0}>{t('Use subscription first')}</option>
                <option value="wallet_first">{t('Use wallet first')}</option>
                <option value="subscription_only" disabled={subscriptions.active.length === 0}>{t('Subscription only')}</option>
                <option value="wallet_only">{t('Wallet only')}</option>
              </select>
            </label>
            {subscriptions.active.length === 0 && (
              <>
                <p className="muted">{t('No active subscriptions.')}</p>
                {(subscriptions.billingPreference === 'subscription_first'
                  || subscriptions.billingPreference === 'subscription_only') && (
                    <p className="muted" role="status">{t(
                      'Preference saved as {{preference}}, but no active subscription. Wallet will be used automatically.',
                      {
                        preference: subscriptions.billingPreference === 'subscription_only'
                          ? t('Subscription only')
                          : t('Use subscription first'),
                      },
                    )}</p>
                  )}
              </>
            )}
            {subscriptions.active.length > 0 && (
              <ul className="wallet-subscriptions">
                {subscriptions.active.map((subscription) => (
                  <SubscriptionRecordItem
                    key={subscription.id}
                    subscription={subscription}
                    plan={plansById.get(subscription.planId)}
                    info={info}
                  />
                ))}
              </ul>
            )}
            <h3>{t('Subscription history')}</h3>
            {historicalSubscriptions.length === 0 ? (
              <p className="muted">{t('No previous subscriptions.')}</p>
            ) : (
              <ul className="wallet-subscriptions wallet-subscription-history">
                {historicalSubscriptions.map((subscription) => (
                  <SubscriptionRecordItem
                    key={subscription.id}
                    subscription={subscription}
                    plan={plansById.get(subscription.planId)}
                    info={info}
                  />
                ))}
              </ul>
            )}
            {plans.length === 0 ? <p className="muted">{t('No subscription plans are available.')}</p> : (
              <div className="wallet-products">
                <h3>{t('Available plans')}</h3>
                {!complianceReady && <p className="muted">{complianceUnavailable}</p>}
                {plans.map((plan) => {
                  const state = planPurchaseState(plan);
                  const purchasesRemaining = plan.maxPurchasePerUser > 0
                    ? Math.max(0, plan.maxPurchasePerUser - state.purchaseCount)
                    : null;
                  let walletUnavailableReason = '';
                  if (state.limitReached) walletUnavailableReason = t('Purchase limit reached.');
                  else if (!plan.allowBalancePay) walletUnavailableReason = t('Wallet purchase is disabled for this plan.');
                  else if (state.balanceCost === null) walletUnavailableReason = t('Unable to calculate the required wallet balance.');
                  else if (state.insufficientBalance) walletUnavailableReason = t('Insufficient wallet balance.');
                  return (
                    <article key={plan.id}>
                      <div>
                        <strong>{plan.title}</strong>
                        <span className="muted">{plan.subtitle}</span>
                        <span>{plan.priceAmount} {plan.currency}</span>
                        <span className="muted">
                          {t('Quota')}: {plan.totalAmount === 0
                            ? t('Unlimited')
                            : info
                              ? formatQuotaForDisplay(plan.totalAmount, info, i18n.language)
                              : number(plan.totalAmount, i18n.language)}
                        </span>
                        <span className="muted">
                          {t('Validity')}: {plan.durationUnit === 'custom'
                            ? `${number(plan.customSeconds, i18n.language)} ${t('Seconds')}`
                            : `${number(plan.durationValue, i18n.language)} ${t(durationUnitKeys[plan.durationUnit])}`}
                        </span>
                        <span className="muted">
                          {t('Quota reset period')}: {plan.quotaResetPeriod === 'custom'
                            ? `${t('Custom')} · ${number(plan.quotaResetCustomSeconds, i18n.language)} ${t('Seconds')}`
                            : t(resetPeriodKeys[plan.quotaResetPeriod])}
                        </span>
                        <span className="muted">
                          {t('Purchase limit per user')}: {plan.maxPurchasePerUser === 0
                            ? t('Unlimited')
                            : number(plan.maxPurchasePerUser, i18n.language)}
                        </span>
                        <span className="muted">{t('Purchases used')}: {plan.maxPurchasePerUser === 0
                          ? number(state.purchaseCount, i18n.language)
                          : t('{{count}} of {{limit}}', {
                            count: state.purchaseCount,
                            limit: plan.maxPurchasePerUser,
                          })}</span>
                        {purchasesRemaining !== null && !state.limitReached && (
                          <span className="muted">{t('{{count}} purchases remaining', { count: purchasesRemaining })}</span>
                        )}
                        {plan.upgradeGroup !== '' && (
                          <span className="muted">{t('Upgrade group')}: {plan.upgradeGroup}</span>
                        )}
                        {plan.downgradeGroup !== '' && (
                          <span className="muted">{t('Downgrade group')}: {plan.downgradeGroup}</span>
                        )}
                        <span className="muted">{t('Wallet overflow')}: {plan.allowWalletOverflow ? t('Enabled') : t('Disabled')}</span>
                        {info && state.balanceCost !== null && (
                          <div className="wallet-plan-balance">
                            <span>{t('Required wallet balance')}: {formatQuotaForDisplay(state.balanceCost, info, i18n.language)}</span>
                            <span>{t('Available wallet balance')}: {formatQuotaForDisplay(walletUser.quota, info, i18n.language)}</span>
                          </div>
                        )}
                        {state.limitReached && (
                          <span className="error" role="status">{t('Purchase limit reached.')} ({state.purchaseCount}/{plan.maxPurchasePerUser})</span>
                        )}
                        {!state.limitReached && walletUnavailableReason !== '' && (
                          <span className="muted">{walletUnavailableReason}</span>
                        )}
                      </div>
                      {complianceReady && <div className="wallet-plan-actions">
                        <span className="muted wallet-plan-action-label">{t('Payment options')}</span>
                        <button
                          type="button"
                          className="link"
                          disabled={Boolean(busy) || state.limitReached || !state.balancePaymentAvailable}
                          title={walletUnavailableReason || undefined}
                          onClick={() => buySubscription(plan)}
                        >
                          {busy === `plan:${plan.id}` ? t('Buying…') : t('Buy with wallet')}
                        </button>
                        {info?.stripeEnabled && plan.stripePriceId !== '' && (
                          <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'stripe')}>{t('Pay with {{method}}', { method: 'Stripe' })}</button>
                        )}
                        {info?.creemEnabled && plan.creemProductId !== '' && (
                          <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'creem')}>{t('Pay with {{method}}', { method: 'Creem' })}</button>
                        )}
                        {info?.waffoPancakeEnabled && plan.waffoPancakeProductId !== '' && (
                          <button type="button" className="link" disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'waffo-pancake')}>{t('Pay with {{method}}', { method: 'Waffo Pancake' })}</button>
                        )}
                        {info?.onlineEnabled && info.paymentMethods.filter((method) => method.type !== 'stripe' && method.type !== 'waffo' && method.type !== 'waffo_pancake').map((method) => (
                          <button type="button" className="link" key={method.type} disabled={Boolean(busy) || state.limitReached} onClick={() => startSubscriptionCheckout(plan, 'epay', method.type)}>{t('Pay with {{method}}', { method: method.name })}</button>
                        ))}
                        {!plan.allowBalancePay && !state.hostedPaymentAvailable && (
                          <span className="muted">{t('No purchase method is currently available for this plan.')}</span>
                        )}
                      </div>}
                    </article>
                  );
                })}
              </div>
            )}
            {complianceReady && subscriptionCheckout && (
              <div className="wallet-checkout" role="status">
                <strong>{t('Subscription checkout ready')}</strong>
                {subscriptionCheckout.orderId && <span className="muted">{t('Order {{id}}', { id: subscriptionCheckout.orderId })}</span>}
                <CheckoutControl checkout={subscriptionCheckout} label={t('Open secure checkout')} />
              </div>
            )}
          </>
        )}
      </section>
    </>
  );
}
