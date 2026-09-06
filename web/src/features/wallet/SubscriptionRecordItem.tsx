import { useTranslation } from 'react-i18next';
import {
  type SubscriptionPlan,
  type UserSubscription,
  type WalletTopUpInfo
} from './wallet-api';
import { dateTime, formatQuotaForDisplay, knownProviderName, number, subscriptionStatusKeys } from './wallet-presentation';

export function SubscriptionRecordItem({
  subscription,
  plan,
  info,
}: {
  subscription: UserSubscription;
  plan: SubscriptionPlan | undefined;
  info: WalletTopUpInfo | null;
}) {
  const { t, i18n } = useTranslation();
  const now = Date.now() / 1_000;
  const effectiveStatus = subscription.status === 'active' && subscription.endTime <= now
    ? 'expired'
    : subscription.status;
  const finiteQuota = subscription.amountTotal > 0;
  const remainingQuota = finiteQuota
    ? Math.max(0, subscription.amountTotal - subscription.amountUsed)
    : 0;
  const usagePercent = finiteQuota
    ? Math.min(100, Math.max(0, Math.round((subscription.amountUsed / subscription.amountTotal) * 100)))
    : 0;
  const remainingDays = effectiveStatus === 'active'
    ? Math.max(0, Math.ceil((subscription.endTime - now) / 86_400))
    : 0;
  const endLabel = effectiveStatus === 'active'
    ? t('Until')
    : effectiveStatus === 'cancelled' ? t('Cancelled at') : t('Expired at');
  const quotaText = (value: number) => info
    ? formatQuotaForDisplay(value, info, i18n.language)
    : number(value, i18n.language);
  return (
    <li>
      <div className="wallet-subscription-heading">
        <strong>
          {plan?.title && <span>{plan.title} · </span>}
          <span>{t('Subscription {{id}}', { id: subscription.id })}</span>
        </strong>
        <span className={`status status-${effectiveStatus}`}>{t(subscriptionStatusKeys[effectiveStatus])}</span>
      </div>
      {effectiveStatus === 'active' && (
        <span className="muted">{t('{{count}} days remaining', { count: remainingDays })}</span>
      )}
      <span>{!finiteQuota
        ? t('Unlimited quota')
        : t('{{used}} of {{total}} used', {
          used: quotaText(subscription.amountUsed),
          total: quotaText(subscription.amountTotal),
        })}</span>
      {finiteQuota && (
        <>
          <span className="muted">{t('Remaining quota')}: {quotaText(remainingQuota)} · {t('Used {{percent}}%', { percent: usagePercent })}</span>
          {effectiveStatus === 'active' && (
            <progress
              className="wallet-subscription-progress"
              aria-label={t('Quota usage for subscription {{id}}', { id: subscription.id })}
              max={100}
              value={usagePercent}
            />
          )}
        </>
      )}
      <span className="muted">{t('Started')}: {dateTime(subscription.startTime, i18n.language)}</span>
      <span className="muted">{endLabel}: {dateTime(subscription.endTime, i18n.language)}</span>
      {effectiveStatus === 'active' && subscription.nextResetTime > 0 && (
        <span className="muted">{t('Next reset')}: {dateTime(subscription.nextResetTime, i18n.language)}</span>
      )}
      {subscription.lastResetTime > 0 && (
        <span className="muted">{t('Last reset')}: {dateTime(subscription.lastResetTime, i18n.language)}</span>
      )}
      {subscription.upgradeGroup !== '' && (
        <span className="muted">{t('Upgrade group')}: {subscription.upgradeGroup}</span>
      )}
      {subscription.downgradeGroup !== '' && (
        <span className="muted">{t('Downgrade group')}: {subscription.downgradeGroup}</span>
      )}
      <span className="muted">{t('Wallet overflow')}: {subscription.allowWalletOverflow ? t('Enabled') : t('Disabled')}</span>
      {subscription.source !== '' && <span className="muted">{t('Source')}: {knownProviderName(subscription.source)}</span>}
    </li>
  );
}
