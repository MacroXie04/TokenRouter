import type { ProfileController } from './useProfileController';

type ProfileDangerPanelProps = Pick<ProfileController,
  'busy' | 'deleteAcknowledged' | 'deleteConfirmation' | 'deleteOwnAccount'
  | 'profile' | 'setDeleteAcknowledged' | 'setDeleteConfirmation' | 't'
>;

export function ProfileDangerPanel({
  busy, deleteAcknowledged, deleteConfirmation, deleteOwnAccount,
  profile, setDeleteAcknowledged, setDeleteConfirmation, t,
}: ProfileDangerPanelProps) {
  if (!profile.data) return <p className="muted">{t('Account deletion is unavailable until your profile loads.')}</p>;
  if (profile.data.role === 100) {
    return <p className="muted">{t('The root administrator account cannot be deleted.')}</p>;
  }
  return (
    <form className="grid-form" aria-label={t('Delete account')} onSubmit={deleteOwnAccount}>
      <p className="error">
        {t('Deleting your account revokes its sessions and credentials. You cannot undo this action from the application.')}
      </p>
      <label>
        {t('Type {{username}} to confirm', { username: profile.data.username })}
        <input
          value={deleteConfirmation}
          onChange={(event) => setDeleteConfirmation(event.target.value)}
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          disabled={busy !== null}
        />
      </label>
      <label>
        <input
          type="checkbox"
          checked={deleteAcknowledged}
          onChange={(event) => setDeleteAcknowledged(event.target.checked)}
          disabled={busy !== null}
        />
        {t('I understand this account action cannot be undone here.')}
      </label>
      <button
        type="submit"
        disabled={busy !== null || deleteConfirmation !== profile.data.username || !deleteAcknowledged}
      >
        {t('Delete account')}
      </button>
    </form>
  );
}
