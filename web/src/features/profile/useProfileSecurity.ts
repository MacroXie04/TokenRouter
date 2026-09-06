import {
  useCallback,
  useState,
  type FormEvent
} from 'react';
import {
  beginPasskeyRegistration,
  changePassword,
  deletePasskeys,
  disableTwoFactor,
  enableTwoFactor,
  finishPasskeyRegistration,
  generateAccessToken,
  getPasskeys,
  getPasskeyStatus,
  getTwoFactorStatus,
  regenerateBackupCodes,
  startTwoFactorSetup,
  verifyTwoFactor,
  type TwoFactorSetup,
} from './profile-api';
import { resource, validFactorCode, validProofCode, type Resource, type SecurityOverview } from './profile-view-model';
import {
  passkeyRegistrationSupported,
  preparePasskeyCreationOptions,
  serializePasskeyRegistrationCredential,
} from './profile-webauthn';
import type { useProfileMutation } from './useProfileMutation';
import type { useProfileSessions } from './useProfileSessions';

type ProfileSecurityOptions = Pick<ReturnType<typeof useProfileSessions>, 'loadSessions'> & Pick<ReturnType<typeof useProfileMutation>, 'mounted' | 'runMutation' | 'setNotice' | 't'>;

export function useProfileSecurity({
  loadSessions, mounted, runMutation, setNotice,
  t,
}: ProfileSecurityOptions) {
  const [security, setSecurity] = useState<Resource<SecurityOverview>>(resource);
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [twoFactorSetup, setTwoFactorSetup] = useState<TwoFactorSetup | null>(null);
  const [twoFactorCode, setTwoFactorCode] = useState('');
  const [disableCode, setDisableCode] = useState('');
  const [disableConfirmed, setDisableConfirmed] = useState(false);
  const [regenerateCode, setRegenerateCode] = useState('');
  const [regenerateConfirmed, setRegenerateConfirmed] = useState(false);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [backupCodesSaved, setBackupCodesSaved] = useState(false);
  const [passkeyCode, setPasskeyCode] = useState('');
  const [accessToken, setAccessToken] = useState<string | null>(null);
  const [accessTokenConfirmed, setAccessTokenConfirmed] = useState(false);

  const loadSecurity = useCallback(async (signal?: AbortSignal) => {
    if (mounted.current && !signal?.aborted) setSecurity((previous) => ({ ...previous, loading: true, error: false }));
    try {
      const [twoFactor, passkey, passkeys] = await Promise.all([
        getTwoFactorStatus(signal),
        getPasskeyStatus(signal),
        getPasskeys(signal),
      ]);
      if (passkey.enabled !== (passkeys.length > 0)) throw new Error('inconsistent passkey state');
      if (!mounted.current || signal?.aborted) return;
      setSecurity({
        data: {
          twoFactorEnabled: twoFactor.enabled,
          passkeyEnabled: passkey.enabled,
          passkeys,
        },
        loading: false,
        error: false,
      });
    } catch {
      if (mounted.current && !signal?.aborted) setSecurity((previous) => ({ ...previous, loading: false, error: true }));
    }
  }, [mounted]);

  const updateSecurityState = useCallback((update: (value: SecurityOverview) => SecurityOverview) => {
    setSecurity((previous) => previous.data
      ? { ...previous, data: update(previous.data), error: false }
      : previous);
  }, []);

  async function savePassword(event: FormEvent) {
    event.preventDefault();
    if (!oldPassword || oldPassword.length > 128) {
      setNotice({ kind: 'error', text: t('Enter your current password.') });
      return;
    }
    if (newPassword.length < 8 || newPassword.length > 64) {
      setNotice({ kind: 'error', text: t('New password must be 8–64 characters.') });
      return;
    }
    if (newPassword !== confirmPassword) {
      setNotice({ kind: 'error', text: t('New passwords do not match.') });
      return;
    }
    const current = oldPassword;
    const replacement = newPassword;
    setOldPassword('');
    setNewPassword('');
    setConfirmPassword('');
    const result = await runMutation('password', t('Unable to change your password.'),
      (signal) => changePassword(current, replacement, signal));
    if (!result.ok) return;
    setNotice({ kind: 'success', text: t('Password changed. Other login sessions were signed out.') });
    void loadSessions();
  }

  async function replaceAccessToken(event: FormEvent) {
    event.preventDefault();
    if (!accessTokenConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that your existing access token will stop working.') });
      return;
    }
    // Never retain a previous one-time credential while requesting its replacement.
    setAccessToken(null);
    setAccessTokenConfirmed(false);
    const result = await runMutation('access-token', t('Unable to generate a new access token.'), generateAccessToken);
    if (!result.ok) return;
    setAccessToken(result.value);
    setNotice({ kind: 'success', text: t('New access token generated. Save it now; it will not be shown again.') });
  }

  async function copyAccessToken() {
    const token = accessToken;
    if (!token) return;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(token);
      if (mounted.current && accessToken === token) {
        setNotice({ kind: 'success', text: t('Access token copied.') });
      }
    } catch {
      if (mounted.current) setNotice({ kind: 'error', text: t('Unable to copy the access token.') });
    }
  }

  function hideAccessToken() {
    setAccessToken(null);
    setNotice({ kind: 'success', text: t('Access token hidden.') });
  }

  async function beginTwoFactorSetup() {
    const result = await runMutation('twofa-start', t('Unable to start two-factor setup.'), startTwoFactorSetup);
    if (!result.ok) return;
    setTwoFactorSetup(result.value);
    setTwoFactorCode('');
    setNotice({ kind: 'success', text: t('Authenticator setup is ready.') });
  }

  function cancelTwoFactorSetup() {
    if (!window.confirm(t('Cancel two-factor setup? The displayed secret will be hidden.'))) return;
    setTwoFactorSetup(null);
    setTwoFactorCode('');
    setNotice(null);
  }

  async function completeTwoFactorSetup(event: FormEvent) {
    event.preventDefault();
    const code = twoFactorCode.trim();
    if (!validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code.') });
      return;
    }
    setTwoFactorCode('');
    const result = await runMutation('twofa-enable', t('Unable to enable two-factor authentication.'),
      (signal) => enableTwoFactor(code, signal));
    if (!result.ok) return;
    setTwoFactorSetup(null);
    setBackupCodes(result.value);
    setBackupCodesSaved(false);
    updateSecurityState((value) => ({ ...value, twoFactorEnabled: true }));
    setNotice({ kind: 'success', text: t('Two-factor authentication enabled. Save the recovery codes now.') });
  }

  async function turnOffTwoFactor(event: FormEvent) {
    event.preventDefault();
    const code = disableCode.trim();
    if (!validFactorCode(code)) {
      setNotice({ kind: 'error', text: t('Enter a valid authenticator or backup code.') });
      return;
    }
    if (!disableConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that you understand two-factor protection will be removed.') });
      return;
    }
    setDisableCode('');
    setDisableConfirmed(false);
    const result = await runMutation('twofa-disable', t('Unable to disable two-factor authentication.'),
      (signal) => disableTwoFactor(code, signal));
    if (!result.ok) return;
    setBackupCodes([]);
    updateSecurityState((value) => ({ ...value, twoFactorEnabled: false }));
    setNotice({ kind: 'success', text: t('Two-factor authentication disabled.') });
  }

  async function replaceBackupCodes(event: FormEvent) {
    event.preventDefault();
    const code = regenerateCode.trim();
    if (!validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code.') });
      return;
    }
    if (!regenerateConfirmed) {
      setNotice({ kind: 'error', text: t('Confirm that existing backup codes will stop working.') });
      return;
    }
    setRegenerateCode('');
    setRegenerateConfirmed(false);
    const result = await runMutation('twofa-backup', t('Unable to regenerate backup codes.'), async (signal) => {
      const proof = await verifyTwoFactor('twofa.backup_codes.regenerate', code, signal);
      return regenerateBackupCodes(proof, signal);
    });
    if (!result.ok) return;
    setBackupCodes(result.value);
    setBackupCodesSaved(false);
    setNotice({ kind: 'success', text: t('New backup codes generated. Previous codes no longer work.') });
  }

  function hideBackupCodes() {
    if (!backupCodesSaved) {
      setNotice({ kind: 'error', text: t('Confirm that you saved the backup codes before hiding them.') });
      return;
    }
    setBackupCodes([]);
    setBackupCodesSaved(false);
    setNotice({ kind: 'success', text: t('Backup codes hidden.') });
  }

  async function registerPasskey() {
    if (!passkeyRegistrationSupported()) {
      setNotice({ kind: 'error', text: t('Passkey registration is not supported on this device.') });
      return;
    }
    const twoFactorEnabled = security.data?.twoFactorEnabled === true;
    const code = passkeyCode.trim();
    if (twoFactorEnabled && !validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code for this security change.') });
      return;
    }
    setPasskeyCode('');
    const result = await runMutation(
      'passkey-register',
      (error) => error instanceof DOMException && error.name === 'NotAllowedError'
        ? t('Passkey registration was canceled.')
        : t('Unable to register a passkey.'),
      async (signal) => {
        const proof = twoFactorEnabled
          ? await verifyTwoFactor('passkey.register', code, signal)
          : undefined;
        const begin = await beginPasskeyRegistration(proof, signal);
        const publicKey = preparePasskeyCreationOptions(begin.publicKey);
        const credential = await navigator.credentials.create({ publicKey } as unknown as CredentialCreationOptions);
        if (!credential) throw new DOMException('canceled', 'NotAllowedError');
        if (signal.aborted) throw new DOMException('aborted', 'AbortError');
        const serialized = serializePasskeyRegistrationCredential(credential);
        await finishPasskeyRegistration(begin.flowToken, serialized, signal);
      },
    );
    if (!result.ok) return;
    updateSecurityState((value) => ({ ...value, passkeyEnabled: true }));
    setNotice({ kind: 'success', text: t('Passkey registered.') });
    void loadSecurity();
  }

  async function removePasskey() {
    if (!window.confirm(t('Remove all passkeys from this account?'))) return;
    const twoFactorEnabled = security.data?.twoFactorEnabled === true;
    const code = passkeyCode.trim();
    if (twoFactorEnabled && !validProofCode(code)) {
      setNotice({ kind: 'error', text: t('Enter the 6-digit authenticator code for this security change.') });
      return;
    }
    setPasskeyCode('');
    const result = await runMutation('passkey-delete', t('Unable to remove the passkey.'), async (signal) => {
      const proof = twoFactorEnabled
        ? await verifyTwoFactor('passkey.delete', code, signal)
        : undefined;
      await deletePasskeys(proof, signal);
    });
    if (!result.ok) return;
    updateSecurityState((value) => ({ ...value, passkeyEnabled: false, passkeys: [] }));
    setNotice({ kind: 'success', text: t('Passkey removed.') });
  }
  return {
    security, setSecurity, oldPassword, setOldPassword,
    newPassword, setNewPassword, confirmPassword, setConfirmPassword,
    twoFactorSetup, setTwoFactorSetup, twoFactorCode, setTwoFactorCode,
    disableCode, setDisableCode, disableConfirmed, setDisableConfirmed,
    regenerateCode, setRegenerateCode, regenerateConfirmed, setRegenerateConfirmed,
    backupCodes, setBackupCodes, backupCodesSaved, setBackupCodesSaved,
    passkeyCode, setPasskeyCode, accessToken, setAccessToken,
    accessTokenConfirmed, setAccessTokenConfirmed, loadSecurity, updateSecurityState,
    savePassword, replaceAccessToken, copyAccessToken, hideAccessToken,
    beginTwoFactorSetup, cancelTwoFactorSetup, completeTwoFactorSetup, turnOffTwoFactor,
    replaceBackupCodes, hideBackupCodes, registerPasskey, removePasskey,
  };
}
