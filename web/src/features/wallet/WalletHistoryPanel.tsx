import type { WalletViewController } from './useWalletController';
import {
  dateTime,
  formatCreditForDisplay,
  HISTORY_PAGE_SIZES,
  money,
  number,
  orderProviderText,
} from './wallet-presentation';

type WalletHistoryPanelProps = Pick<WalletViewController,
  'busy' | 'changeHistoryPageSize' | 'completeOrder' | 'copiedKey'
  | 'copyValue' | 'historyKeyword' | 'historyPage' | 'historyPageSize'
  | 'historySectionRef' | 'i18n' | 'info' | 'isAdmin'
  | 'loadOrders' | 'orders' | 'ordersError' | 'ordersLoading'
  | 'searchHistory' | 'setHistoryKeyword' | 't'
>;

export function WalletHistoryPanel({
  busy, changeHistoryPageSize, completeOrder, copiedKey,
  copyValue, historyKeyword, historyPage, historyPageSize,
  historySectionRef, i18n, info, isAdmin,
  loadOrders, orders, ordersError, ordersLoading,
  searchHistory, setHistoryKeyword, t,
}: WalletHistoryPanelProps) {
  return (
    <>
      <section
        ref={historySectionRef}
        id="wallet-payment-history"
        className="card wallet-history-target"
        aria-labelledby="wallet-payment-history-heading"
        tabIndex={-1}
      >
        <div className="wallet-section-heading">
          <div>
            <h2 id="wallet-payment-history-heading">{t('Payment history')}</h2>
            <p className="muted">{t('Recent wallet orders from the last 30 days.')}</p>
          </div>
          <button type="button" className="link" onClick={() => loadOrders(historyPage, historyPageSize, historyKeyword)} disabled={ordersLoading}>{t('Refresh')}</button>
        </div>
        <form className="inline-form wallet-search wallet-history-controls" onSubmit={searchHistory}>
          <label>{t('Search orders')}<input value={historyKeyword} onChange={(event) => setHistoryKeyword(event.target.value)} maxLength={255} /></label>
          <label>
            {t('Rows per page')}
            <select
              aria-label={t('Rows per page')}
              value={historyPageSize}
              disabled={ordersLoading}
              onChange={(event) => changeHistoryPageSize(event.target.value)}
            >
              {HISTORY_PAGE_SIZES.map((size) => (
                <option key={size} value={size}>{t('{{count}} per page', { count: size })}</option>
              ))}
            </select>
          </label>
          <button type="submit" disabled={ordersLoading}>{t('Search')}</button>
        </form>
        {ordersLoading && <p className="muted" role="status">{t('Loading payment history…')}</p>}
        {ordersError && (
          <div className="error-panel" role="alert">
            <p>{t('Unable to load payment history.')}</p>
            <button type="button" className="link" onClick={() => loadOrders(historyPage, historyPageSize, historyKeyword)}>{t('Try again')}</button>
          </div>
        )}
        {!ordersLoading && !ordersError && orders?.items.length === 0 && <p className="muted">{t('No wallet orders match this search.')}</p>}
        {!ordersLoading && !ordersError && orders && orders.items.length > 0 && (
          <div className="wallet-table-wrap">
            <table className="wallet-table">
              <caption className="sr-only">{t('Payment history')}</caption>
              <thead><tr><th>{t('Order')}</th>{isAdmin && <th>{t('User ID')}</th>}<th>{t('Provider')}</th><th>{t('Amount')}</th><th>{t('Paid amount')}</th><th>{t('Status')}</th><th>{t('Created')}</th>{isAdmin && <th>{t('Actions')}</th>}</tr></thead>
              <tbody>
                {orders.items.map((order) => (
                  <tr key={order.id}>
                    <td data-label={t('Order')}>
                      <div className="wallet-copy-value">
                        <code>{order.tradeNo}</code>
                        <button
                          type="button"
                          className="link"
                          aria-label={t('Copy order number')}
                          onClick={() => { void copyValue(order.tradeNo, `order:${order.id}`, t('Order number copied.')); }}
                        >
                          {copiedKey === `order:${order.id}` ? t('Copied') : t('Copy')}
                        </button>
                      </div>
                    </td>
                    {isAdmin && <td data-label={t('User ID')}>
                      <div className="wallet-copy-value">
                        <span>{number(order.userId, i18n.language)}</span>
                        <button
                          type="button"
                          className="link"
                          aria-label={t('Copy user ID')}
                          onClick={() => { void copyValue(String(order.userId), `user:${order.id}`, t('User ID copied.')); }}
                        >
                          {copiedKey === `user:${order.id}` ? t('Copied') : t('Copy')}
                        </button>
                      </div>
                    </td>}
                    <td data-label={t('Provider')}>{orderProviderText(order, info)}</td>
                    <td data-label={t('Amount')}>{info
                      ? formatCreditForDisplay(order.amount, info, i18n.language)
                      : number(order.amount, i18n.language)}</td>
                    <td data-label={t('Paid amount')}>{money(order.money, i18n.language)}</td>
                    <td data-label={t('Status')}><span className={`status status-${order.status}`}>{t(order.status)}</span></td>
                    <td data-label={t('Created')}>
                      <span>{dateTime(order.createdAt, i18n.language)}</span>
                      {order.completedAt > 0 && (
                        <small className="muted">{t('Completed')}: {dateTime(order.completedAt, i18n.language)}</small>
                      )}
                    </td>
                    {isAdmin && (
                      <td data-label={t('Actions')}>
                        {order.status === 'pending' && (
                          <button
                            type="button"
                            className="link"
                            disabled={Boolean(busy)}
                            onClick={() => { void completeOrder(order); }}
                          >
                            {busy === `complete-order:${order.id}` ? t('Processing...') : t('Complete Order')}
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
            <nav className="pagination" aria-label={t('Payment history pages')}>
              <button type="button" className="link" disabled={orders.page <= 1 || ordersLoading} onClick={() => loadOrders(orders.page - 1, historyPageSize, historyKeyword)}>{t('Previous')}</button>
              <span>
                {t('Showing {{start}}–{{end}} of {{total}}', {
                  start: (orders.page - 1) * orders.pageSize + 1,
                  end: Math.min(orders.page * orders.pageSize, orders.total),
                  total: orders.total,
                })} · {t('Page {{page}} of {{pages}}', { page: orders.page, pages: Math.max(1, Math.ceil(orders.total / orders.pageSize)) })}
              </span>
              <button type="button" className="link" disabled={orders.page * orders.pageSize >= orders.total || ordersLoading} onClick={() => loadOrders(orders.page + 1, historyPageSize, historyKeyword)}>{t('Next')}</button>
            </nav>
          </div>
        )}
      </section>
    </>
  );
}
