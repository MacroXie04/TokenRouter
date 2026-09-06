import { useEffect } from 'react';
import { defaultExternalNavigate } from './profile-view-model';
import type { ProfileViewProps } from './profile-view-props';
import { useProfileConnections } from './useProfileConnections';
import { useProfileDeletion } from './useProfileDeletion';
import { useProfileMutation } from './useProfileMutation';
import { useProfilePreferences } from './useProfilePreferences';
import { useProfileSecurity } from './useProfileSecurity';
import { useProfileSessions } from './useProfileSessions';

export function useProfileController({
  onNavigate,
  onProfileChange,
  onExternalNavigate = defaultExternalNavigate,
}: ProfileViewProps) {
  // Every section shares one mutation lock and cancellation lifecycle.
  const mutation = useProfileMutation();
  const { mounted, runMutation, setNotice, t, i18nRef, abortActiveMutation } = mutation;
  const common = { mounted, runMutation, setNotice, t };
  const sessions = useProfileSessions(common);
  const preferences = useProfilePreferences({ ...common, i18nRef, onProfileChange });
  const security = useProfileSecurity({ ...common, loadSessions: sessions.loadSessions });
  const connections = useProfileConnections({
    ...common,
    onExternalNavigate,
    onProfileChange,
    profile: preferences.profile,
    setProfile: preferences.setProfile,
  });
  const deletion = useProfileDeletion({
    ...common,
    onNavigate,
    profile: preferences.profile,
    setAccessToken: security.setAccessToken,
  });
  const { loadOAuth } = connections;
  const { loadProfile } = preferences;
  const { loadSecurity } = security;
  const { loadSessions } = sessions;

  useEffect(() => {
    mounted.current = true;
    const controller = new AbortController();
    void loadProfile(controller.signal);
    void loadSecurity(controller.signal);
    void loadSessions(controller.signal);
    void loadOAuth(controller.signal);
    return () => {
      mounted.current = false;
      controller.abort();
      abortActiveMutation();
    };
  }, [abortActiveMutation, loadOAuth, loadProfile, loadSecurity, loadSessions, mounted]);

  return { ...mutation, ...sessions, ...preferences, ...security, ...connections, ...deletion, onNavigate };
}

export type ProfileController = ReturnType<typeof useProfileController>;
