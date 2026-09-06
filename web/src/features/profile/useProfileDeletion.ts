import {
  useState,
  type FormEvent
} from 'react';
import { SESSION_EXPIRED_EVENT } from '../../shared/api/client';
import {
  deleteAccount
} from './profile-api';
import type { ProfileViewProps } from './profile-view-props';
import type { useProfileMutation } from './useProfileMutation';
import type { useProfilePreferences } from './useProfilePreferences';
import type { useProfileSecurity } from './useProfileSecurity';

type ProfileDeletionOptions = Pick<ProfileViewProps,
  'onNavigate'
> & Pick<ReturnType<typeof useProfilePreferences>, 'profile'> & Pick<ReturnType<typeof useProfileMutation>, 'runMutation' | 'setNotice' | 't'> & Pick<ReturnType<typeof useProfileSecurity>, 'setAccessToken'>;

export function useProfileDeletion({
  onNavigate, profile, runMutation, setAccessToken,
  setNotice, t,
}: ProfileDeletionOptions) {
  const [deleteConfirmation, setDeleteConfirmation] = useState('');
  const [deleteAcknowledged, setDeleteAcknowledged] = useState(false);

  async function deleteOwnAccount(event: FormEvent) {
    event.preventDefault();
    const account = profile.data;
    if (!account || account.role === 100) return;
    if (deleteConfirmation !== account.username || !deleteAcknowledged) {
      setNotice({ kind: 'error', text: t('Enter your exact username and acknowledge the permanent account action.') });
      return;
    }
    setDeleteConfirmation('');
    setDeleteAcknowledged(false);
    setAccessToken(null);
    const result = await runMutation('account-delete', t('Unable to delete your account.'), deleteAccount);
    if (!result.ok) return;
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    onNavigate?.('/sign-in');
  }
  return { deleteConfirmation, setDeleteConfirmation, deleteAcknowledged, setDeleteAcknowledged, deleteOwnAccount };
}
