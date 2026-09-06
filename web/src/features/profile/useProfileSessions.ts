import {
  useCallback,
  useState
} from 'react';
import {
  getProfile,
  getSessions,
  revokeOtherSessions,
  revokeSession,
  type LoginSession,
} from './profile-api';
import { compactIdentifier, resource, type Resource } from './profile-view-model';
import type { useProfileMutation } from './useProfileMutation';

export function useProfileSessions({
  mounted, runMutation, setNotice, t,
}: Pick<ReturnType<typeof useProfileMutation>, 'mounted' | 'runMutation' | 'setNotice' | 't'>) {
  const [sessions, setSessions] = useState<Resource<LoginSession[]>>(resource);

  const loadSessions = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setSessions((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const next = await getSessions(signal);
      if (mounted.current && !signal?.aborted) setSessions({ data: next, loading: false, error: false });
    } catch {
      if (mounted.current && !signal?.aborted) setSessions((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, [mounted]);

  async function signOutSession(session: LoginSession) {
    if (!window.confirm(t('Sign out session {{session}}? It may be this browser.', {
      session: compactIdentifier(session.sid),
    }))) return;
    const result = await runMutation('session-revoke', t('Unable to sign out that session.'),
      (signal) => revokeSession(session.sid, signal));
    if (!result.ok) return;
    setSessions((previous) => ({
      data: previous.data?.filter((item) => item.sid !== session.sid) ?? [],
      loading: false,
      error: false,
    }));
    setNotice({ kind: 'success', text: t('Session signed out.') });
    // If this was the current session, the authenticated probe receives a 401
    // and the shared API interceptor invalidates the app shell immediately.
    void getProfile().catch(() => undefined);
  }

  async function signOutOtherSessions() {
    if (!window.confirm(t('Sign out every other session? This browser will stay signed in.'))) return;
    const result = await runMutation('sessions-revoke-others', t('Unable to sign out other sessions.'), async (signal) => {
      await revokeOtherSessions(signal);
      return getSessions(signal);
    });
    if (!result.ok) return;
    setSessions({ data: result.value, loading: false, error: false });
    setNotice({ kind: 'success', text: t('Other sessions signed out.') });
  }
  return { sessions, setSessions, loadSessions, signOutSession, signOutOtherSessions };
}
