import { compactIdentifier } from './profile-view-model';
import { EmptyOrFailure } from './ProfileResourceState';
import type { ProfileController } from './useProfileController';

type ProfileConnectionsPanelProps = Pick<ProfileController,
  'beginTelegramBinding' | 'busy' | 'connectOAuth' | 'connectWeChat'
  | 'disconnectOAuth' | 'loadOAuth' | 'oauth' | 'profile'
  | 'setWeChatCode' | 't' | 'telegramFlow' | 'telegramWidget'
  | 'weChatCode'
>;

export function ProfileConnectionsPanel({
  beginTelegramBinding, busy, connectOAuth, connectWeChat,
  disconnectOAuth, loadOAuth, oauth, profile,
  setWeChatCode, t, telegramFlow, telegramWidget,
  weChatCode,
}: ProfileConnectionsPanelProps) {
  if (oauth.loading || oauth.error || !oauth.data) {
    return (
      <EmptyOrFailure
        loading={oauth.loading}
        error={oauth.error}
        loadingText={t('Loading connected accounts…')}
        errorText={t('Unable to load connected accounts. Connection changes are disabled.')}
        onRetry={() => { void loadOAuth(); }}
      />
    );
  }
  const { catalog, bindings } = oauth.data;
  const knownProviderIds = new Set(catalog.customProviders.map((provider) => provider.id));
  const enabledBuiltIns = catalog.builtIn.filter((provider) => provider.enabled);
  const hasAnything = enabledBuiltIns.length > 0 || catalog.weChatEnabled
    || catalog.telegramEnabled || catalog.customProviders.length > 0 || bindings.length > 0;
  if (!hasAnything) return <p className="muted">{t('No account-connection providers are available.')}</p>;

  return (
    <>
      {enabledBuiltIns.length > 0 && (
        <div className="user-subpanel">
          <h3>{t('Built-in sign-in providers')}</h3>
          <ul>
            {enabledBuiltIns.map((provider) => <li key={provider.name}>{provider.name} · {t('Available')}</li>)}
          </ul>
          <p className="muted">
            {t('Connection and removal controls for these built-in providers are not available here.')}
          </p>
        </div>
      )}

      {catalog.customProviders.length > 0 && (
        <div className="user-subpanel">
          <h3>{t('Organization sign-in providers')}</h3>
          <ul className="key-list">
            {catalog.customProviders.map((provider) => {
              const binding = bindings.find((item) => item.providerId === provider.id);
              return (
                <li key={provider.id}>
                  <strong>{provider.name}</strong>
                  <span className="muted">
                    {binding
                      ? t('Connected as {{account}}', { account: compactIdentifier(binding.providerUserId) })
                      : t('Not connected')}
                  </span>
                  {binding ? (
                    <button type="button" className="link" onClick={() => { void disconnectOAuth(binding); }} disabled={busy !== null}>
                      {t('Disconnect')}
                    </button>
                  ) : (
                    <button type="button" onClick={() => { void connectOAuth(provider.id); }} disabled={busy !== null}>
                      {t('Connect')}
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {bindings.some((binding) => !knownProviderIds.has(binding.providerId)) && (
        <div className="user-subpanel">
          <h3>{t('Inactive provider connections')}</h3>
          <ul className="key-list">
            {bindings.filter((binding) => !knownProviderIds.has(binding.providerId)).map((binding) => (
              <li key={binding.providerId}>
                <strong>{binding.providerName}</strong>
                <span className="muted">{t('Connected; new sign-ins are currently unavailable.')}</span>
                <button type="button" className="link" onClick={() => { void disconnectOAuth(binding); }} disabled={busy !== null}>
                  {t('Disconnect')}
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {catalog.telegramEnabled && catalog.telegramBotName && (
        <div className="user-subpanel">
          <h3>{t('Telegram')}</h3>
          <p className="muted">
            {profile.data?.telegramConnected
              ? t('Telegram is connected to this account.')
              : t('Start a one-time Telegram binding flow.')}
          </p>
          {!profile.data?.telegramConnected && (
            <button
              type="button"
              onClick={() => { void beginTelegramBinding(); }}
              disabled={busy !== null || profile.loading || profile.error}
            >
              {telegramFlow ? t('Restart Telegram binding') : t('Start Telegram binding')}
            </button>
          )}
          {!profile.data?.telegramConnected && telegramFlow && (
            <div className="user-subpanel" role="status" aria-live="polite">
              <p>{t('Complete the connection with Telegram below. The one-time flow expires after a short time.')}</p>
              <div ref={telegramWidget} role="group" aria-label={t('Telegram sign-in control')} />
            </div>
          )}
        </div>
      )}

      {catalog.weChatEnabled && (
        <form className="grid-form" aria-label={t('Connect WeChat')} onSubmit={connectWeChat}>
          {catalog.weChatQRCode && (
            <img src={catalog.weChatQRCode} alt={t('WeChat authorization QR code')} referrerPolicy="no-referrer" />
          )}
          <label>
            {t('WeChat authorization code')}
            <input
              value={weChatCode}
              onChange={(event) => setWeChatCode(event.target.value)}
              required
              maxLength={512}
              autoComplete="off"
              disabled={busy !== null}
            />
          </label>
          <button type="submit" disabled={busy !== null}>{t('Connect WeChat')}</button>
          <p className="muted">{t('This endpoint can connect WeChat, but it does not report whether WeChat is already connected.')}</p>
        </form>
      )}
    </>
  );
}
