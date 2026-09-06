import {
  useCallback,
  useRef,
  useState
} from 'react';
import { useTranslation } from 'react-i18next';
import { type MutationResult, type Notice } from './profile-view-model';

export function useProfileMutation() {
  const { t, i18n } = useTranslation();

  const i18nRef = useRef(i18n);

  i18nRef.current = i18n;

  const mounted = useRef(true);
  const mutationLocked = useRef(false);
  const activeMutation = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<Notice>(null);

  const runMutation = useCallback(async <T,>(
    key: string,
    failureText: string | ((error: unknown) => string),
    operation: (signal: AbortSignal) => Promise<T>,
  ): Promise<MutationResult<T>> => {
    if (mutationLocked.current) return { ok: false };
    mutationLocked.current = true;
    const controller = new AbortController();
    activeMutation.current = controller;
    if (mounted.current) {
      setBusy(key);
      setNotice(null);
    }
    try {
      const value = await operation(controller.signal);
      if (!mounted.current || controller.signal.aborted) return { ok: false };
      return { ok: true, value };
    } catch (error) {
      if (mounted.current && !controller.signal.aborted) {
        const text = typeof failureText === 'function' ? failureText(error) : failureText;
        setNotice({ kind: 'error', text });
      }
      return { ok: false };
    } finally {
      if (activeMutation.current === controller) activeMutation.current = null;
      mutationLocked.current = false;
      if (mounted.current && !controller.signal.aborted) setBusy(null);
    }
  }, []);

  // Abort the currently running operation, including one started after mount.
  const abortActiveMutation = useCallback(() => activeMutation.current?.abort(), []);

  const formattedDate = useCallback((date: Date): string => {
    try {
      return new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
    } catch {
      return t('Unknown time');
    }
  }, [i18n.language, t]);
  return {
    t, i18n, i18nRef, mounted,
    mutationLocked, activeMutation, busy, setBusy,
    notice, setNotice, runMutation, formattedDate,
    abortActiveMutation,
  };
}
