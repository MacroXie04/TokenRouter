import type { FormEvent } from 'react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { checkDeploymentName, estimateDeploymentPrice, loadDeploymentHardware, loadDeploymentReplicas } from "./deployment-api";
import { type DeploymentCreateInput, type DeploymentHardware, type DeploymentPriceEstimate, type DeploymentReplica, type DeploymentSummary, type DeploymentUpdateInput } from "./deployment-contracts";
import { Overlay, type ResourceState } from './models-ui';

export const EMPTY_DEPLOYMENT: DeploymentCreateInput = {
  name: '',
  durationHours: 1,
  GPUsPerContainer: 1,
  hardwareId: 0,
  locationIds: [],
  replicaCount: 1,
  image: '',
  registryUsername: '',
  registrySecret: '',
  trafficPort: 5_000,
};

export function environmentDraft(value: string): Record<string, string> | undefined {
  if (!value.trim()) return undefined;
  const parsed: unknown = JSON.parse(value);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('invalid environment');
  const entries = Object.entries(parsed as Record<string, unknown>);
  if (entries.length === 0 || entries.length > 256 || entries.some(([key, entry]) => !key || typeof entry !== 'string')) {
    throw new Error('invalid environment');
  }
  return Object.fromEntries(entries) as Record<string, string>;
}

export function argumentsDraft(value: string): string[] | undefined {
  const values = value.split(/\s+/u).map((entry) => entry.trim()).filter(Boolean);
  return values.length > 0 ? values : undefined;
}

export function DeploymentCreator({ busy, onCancel, onSave }: {
  busy: boolean;
  onCancel: () => void;
  onSave: (input: DeploymentCreateInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(EMPTY_DEPLOYMENT);
  const [hardware, setHardware] = useState<ResourceState<DeploymentHardware[]>>({ status: 'loading' });
  const [hardwareAttempt, setHardwareAttempt] = useState(0);
  const [replicas, setReplicas] = useState<ResourceState<DeploymentReplica[]>>({ status: 'loading' });
  const [replicaAttempt, setReplicaAttempt] = useState(0);
  const [nameState, setNameState] = useState<'idle' | 'checking' | 'available' | 'unavailable' | 'error'>('idle');
  const [nameAttempt, setNameAttempt] = useState(0);
  const [priceState, setPriceState] = useState<ResourceState<DeploymentPriceEstimate> | { status: 'idle' }>({ status: 'idle' });
  const [priceAttempt, setPriceAttempt] = useState(0);
  const [currency, setCurrency] = useState<'usdc' | 'iocoin'>('usdc');
  const [environmentJSON, setEnvironmentJSON] = useState('');
  const [secretEnvironmentJSON, setSecretEnvironmentJSON] = useState('');
  const [entrypoint, setEntrypoint] = useState('');
  const [args, setArgs] = useState('');
  const [locationError, setLocationError] = useState(false);
  const [configurationError, setConfigurationError] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setHardware({ status: 'loading' });
    void loadDeploymentHardware(controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      const options = result.items.filter((item) => item.available && item.maxGPUs > 0);
      setHardware({ status: 'ready', value: options });
      if (options.length > 0) {
        setDraft((current) => current.hardwareId > 0 ? current : { ...current, hardwareId: options[0].id });
      }
    }).catch(() => {
      if (!controller.signal.aborted) setHardware({ status: 'error' });
    });
    return () => controller.abort();
  }, [hardwareAttempt]);

  const selectedHardware = hardware.status === 'ready'
    ? hardware.value.find((item) => item.id === draft.hardwareId)
    : undefined;

  useEffect(() => {
    if (draft.hardwareId <= 0 || draft.GPUsPerContainer <= 0) {
      setReplicas({ status: 'ready', value: [] });
      return;
    }
    const controller = new AbortController();
    setReplicas({ status: 'loading' });
    void loadDeploymentReplicas(draft.hardwareId, draft.GPUsPerContainer, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      setReplicas({ status: 'ready', value: result });
      const valid = new Set(result.filter((entry) => entry.availableCount > 0).map((entry) => entry.locationId));
      setDraft((current) => ({ ...current, locationIds: current.locationIds.filter((id) => valid.has(id)) }));
    }).catch(() => {
      if (!controller.signal.aborted) setReplicas({ status: 'error' });
    });
    return () => controller.abort();
  }, [draft.GPUsPerContainer, draft.hardwareId, replicaAttempt]);

  useEffect(() => {
    const name = draft.name.trim();
    if (!name) {
      setNameState('idle');
      return;
    }
    const controller = new AbortController();
    setNameState('checking');
    const timeout = window.setTimeout(() => {
      void checkDeploymentName(name, controller.signal).then((available) => {
        if (!controller.signal.aborted) setNameState(available ? 'available' : 'unavailable');
      }).catch(() => {
        if (!controller.signal.aborted) setNameState('error');
      });
    }, 250);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [draft.name, nameAttempt]);

  useEffect(() => {
    if (draft.hardwareId <= 0 || draft.locationIds.length === 0 || draft.GPUsPerContainer <= 0
      || draft.durationHours <= 0 || draft.replicaCount <= 0) {
      setPriceState({ status: 'idle' });
      return;
    }
    const controller = new AbortController();
    setPriceState({ status: 'loading' });
    const timeout = window.setTimeout(() => {
      void estimateDeploymentPrice({
        durationHours: draft.durationHours,
        GPUsPerContainer: draft.GPUsPerContainer,
        hardwareId: draft.hardwareId,
        locationIds: draft.locationIds,
        replicaCount: draft.replicaCount,
      }, currency, controller.signal).then((value) => {
        if (!controller.signal.aborted) setPriceState({ status: 'ready', value });
      }).catch(() => {
        if (!controller.signal.aborted) setPriceState({ status: 'error' });
      });
    }, 200);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [currency, draft.durationHours, draft.GPUsPerContainer, draft.hardwareId, draft.locationIds, draft.replicaCount, priceAttempt]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (draft.locationIds.length === 0 || nameState !== 'available') {
      setLocationError(draft.locationIds.length === 0);
      return;
    }
    try {
      const environmentVariables = environmentDraft(environmentJSON);
      const secretEnvironmentVariables = environmentDraft(secretEnvironmentJSON);
      setConfigurationError(false);
      setLocationError(false);
      void onSave({
        ...draft,
        ...(environmentVariables ? { environmentVariables } : {}),
        ...(secretEnvironmentVariables ? { secretEnvironmentVariables } : {}),
        ...(argumentsDraft(entrypoint) ? { entrypoint: argumentsDraft(entrypoint) } : {}),
        ...(argumentsDraft(args) ? { args: argumentsDraft(args) } : {}),
      });
    } catch {
      setConfigurationError(true);
    }
  }

  return (
    <Overlay title={t('Create deployment')} onClose={onCancel} busy={busy}>
      <form className="models-form" aria-label={t('Create deployment')} onSubmit={submit}>
        <label>{t('Deployment name')}<input autoFocus required maxLength={128} value={draft.name} aria-describedby="deployment-name-state" onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></label>
        <p className={`models-validation-state ${nameState}`} id="deployment-name-state" aria-live="polite">
          {nameState === 'checking' ? t('Checking name…') : nameState === 'available' ? t('Name is available') : nameState === 'unavailable' ? t('Name is not available') : nameState === 'error' ? <>{t('Unable to check this name.')} <button type="button" className="models-inline-link" onClick={() => setNameAttempt((value) => value + 1)}>{t('Retry')}</button></> : ''}
        </p>
        <label>{t('Container image')}<input required maxLength={2_048} placeholder="registry.example/image:tag" value={draft.image} onChange={(e) => setDraft({ ...draft, image: e.target.value })} /></label>
        <div className="models-form-grid">
          <label>{t('Hardware')}
            <select required value={draft.hardwareId || ''} disabled={hardware.status !== 'ready' || hardware.value.length === 0} onChange={(e) => {
              const hardwareId = Number(e.target.value);
              const option = hardware.status === 'ready' ? hardware.value.find((item) => item.id === hardwareId) : undefined;
              setDraft((current) => ({ ...current, hardwareId, locationIds: [], GPUsPerContainer: Math.min(current.GPUsPerContainer, option?.maxGPUs ?? 1) }));
            }}>
              <option value="">{hardware.status === 'loading' ? t('Loading hardware…') : t('Select')}</option>
              {hardware.status === 'ready' && hardware.value.map((item) => <option value={item.id} key={item.id}>{item.brandName ? `${item.brandName} ` : ''}{item.name} · {item.availableCount} {t('Available')}</option>)}
            </select>
          </label>
          <label>{t('GPUs per container')}<input required type="number" min={1} max={selectedHardware?.maxGPUs ?? 1_024} value={draft.GPUsPerContainer} onChange={(e) => setDraft({ ...draft, GPUsPerContainer: Number(e.target.value), locationIds: [] })} /></label>
          <label>{t('Replica count')}<input required type="number" min={1} max={1_000} value={draft.replicaCount} onChange={(e) => setDraft({ ...draft, replicaCount: Number(e.target.value) })} /></label>
          <label>{t('Duration hours')}<input required type="number" min={1} max={43_920} value={draft.durationHours} onChange={(e) => setDraft({ ...draft, durationHours: Number(e.target.value) })} /></label>
          <label>{t('Billing currency')}<select value={currency} onChange={(event) => setCurrency(event.target.value as 'usdc' | 'iocoin')}><option value="usdc">USDC</option><option value="iocoin">IOCOIN</option></select></label>
        </div>
        {hardware.status === 'error' && <div className="models-inline-error" role="alert"><span>{t('Unable to load hardware.')}</span><button type="button" onClick={() => setHardwareAttempt((value) => value + 1)}>{t('Retry')}</button></div>}
        {hardware.status === 'ready' && hardware.value.length === 0 && <p className="models-empty-note">{t('No hardware is available.')}</p>}
        <fieldset className="models-option-fieldset" aria-describedby={locationError ? 'deployment-location-error' : undefined}>
          <legend>{t('Locations')}</legend>
          {replicas.status === 'loading' ? <p role="status">{t('Loading locations…')}</p> : replicas.status === 'error' ? <div className="models-inline-error" role="alert"><span>{t('Unable to load locations.')}</span><button type="button" onClick={() => setReplicaAttempt((value) => value + 1)}>{t('Retry')}</button></div> : replicas.value.length === 0 ? <p>{t('No locations are available for this hardware.')}</p> : <div className="models-checkbox-grid">{replicas.value.map((replica) => <label key={replica.locationId}><input type="checkbox" disabled={replica.availableCount < 1} checked={draft.locationIds.includes(replica.locationId)} onChange={(event) => setDraft((current) => ({ ...current, locationIds: event.target.checked ? [...current.locationIds, replica.locationId] : current.locationIds.filter((id) => id !== replica.locationId) }))} />{replica.locationName} · {replica.availableCount} {t('Available')}</label>)}</div>}
        </fieldset>
        {locationError && <p className="models-field-error" id="deployment-location-error" role="alert">{t('Select one or more available locations.')}</p>}
        <section className="models-price-estimate" aria-live="polite" aria-busy={priceState.status === 'loading'}>
          <strong>{t('Price estimation')}</strong>
          {priceState.status === 'idle' ? <span>{t('Select hardware and locations to estimate the price.')}</span> : priceState.status === 'loading' ? <span>{t('Estimating price…')}</span> : priceState.status === 'error' ? <span>{t('Unable to estimate price.')} <button type="button" className="models-inline-link" onClick={() => setPriceAttempt((value) => value + 1)}>{t('Retry')}</button></span> : <span>{t('Estimated cost')}: {formatPriceEstimate(priceState.value.totalCost)} {priceState.value.currency.toUpperCase()}</span>}
        </section>
        <details className="models-advanced"><summary>{t('Advanced configuration')}</summary><div className="models-form-grid">
          <label>{t('Traffic port')}<input type="number" min={1} max={65_535} value={draft.trafficPort ?? ''} onChange={(e) => setDraft({ ...draft, trafficPort: Number(e.target.value) })} /></label>
          <label>{t('Entrypoint (space separated)')}<input maxLength={4_096} value={entrypoint} onChange={(e) => setEntrypoint(e.target.value)} /></label>
          <label>{t('Arguments (space separated)')}<input maxLength={4_096} value={args} onChange={(e) => setArgs(e.target.value)} /></label>
          <label>{t('Registry username')}<input maxLength={4_096} autoComplete="off" value={draft.registryUsername} onChange={(e) => setDraft({ ...draft, registryUsername: e.target.value })} /></label>
          <label>{t('Registry secret')}<input type="password" maxLength={4_096} autoComplete="new-password" value={draft.registrySecret} onChange={(e) => setDraft({ ...draft, registrySecret: e.target.value })} /></label>
        </div>
          <label>{t('Environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"KEY":"value"}'} value={environmentJSON} aria-invalid={configurationError} onChange={(e) => setEnvironmentJSON(e.target.value)} /></label>
          <label>{t('Secret environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"TOKEN":"value"}'} value={secretEnvironmentJSON} aria-invalid={configurationError} onChange={(e) => setSecretEnvironmentJSON(e.target.value)} /></label>
        </details>
        {configurationError && <p className="models-field-error" role="alert">{t('Configuration must be valid JSON objects with string values.')}</p>}
        <p className="models-security-note">{t('Registry credentials are sent only when you submit and are never displayed again.')}</p>
        <footer className="models-dialog-actions"><button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button><button type="submit" className="primary" disabled={busy || hardware.status !== 'ready' || hardware.value.length === 0 || nameState !== 'available'}>{busy ? t('Creating…') : t('Create deployment')}</button></footer>
      </form>
    </Overlay>
  );
}

export function formatPriceEstimate(value: number): string {
  return new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(value);
}

export function DeploymentUpdater({ deployment, busy, onCancel, onSave }: {
  deployment: DeploymentSummary;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: DeploymentUpdateInput) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [image, setImage] = useState('');
  const [trafficPort, setTrafficPort] = useState('');
  const [command, setCommand] = useState('');
  const [entrypoint, setEntrypoint] = useState('');
  const [args, setArgs] = useState('');
  const [registryUsername, setRegistryUsername] = useState('');
  const [registrySecret, setRegistrySecret] = useState('');
  const [environmentJSON, setEnvironmentJSON] = useState('');
  const [secretEnvironmentJSON, setSecretEnvironmentJSON] = useState('');
  const [error, setError] = useState<'none' | 'empty' | 'configuration'>('none');

  function submit(event: FormEvent) {
    event.preventDefault();
    try {
      const input: DeploymentUpdateInput = {
        ...(image.trim() ? { image } : {}),
        ...(trafficPort ? { trafficPort: Number(trafficPort) } : {}),
        ...(command.trim() ? { command } : {}),
        ...(entrypoint.trim() ? { entrypoint: argumentsDraft(entrypoint) } : {}),
        ...(args.trim() ? { args: argumentsDraft(args) } : {}),
        ...(registryUsername.trim() ? { registryUsername } : {}),
        ...(registrySecret ? { registrySecret } : {}),
        ...(environmentJSON.trim() ? { environmentVariables: environmentDraft(environmentJSON) } : {}),
        ...(secretEnvironmentJSON.trim()
          ? { secretEnvironmentVariables: environmentDraft(secretEnvironmentJSON) }
          : {}),
      };
      if (Object.keys(input).length === 0) {
        setError('empty');
        return;
      }
      setError('none');
      void onSave(input);
    } catch {
      setError('configuration');
    }
  }

  return (
    <Overlay title={t('Update deployment')} onClose={onCancel} busy={busy}>
      <form className="models-form" aria-label={t('Update deployment')} onSubmit={submit}>
        <p className="models-security-note">{t('Only the settings entered below will be changed for {{name}}.', { name: deployment.name })}</p>
        <label>{t('Container image')}<input autoFocus maxLength={2_048} placeholder="registry.example/image:tag" value={image} onChange={(event) => setImage(event.target.value)} /></label>
        <div className="models-form-grid">
          <label>{t('Traffic port')}<input type="number" min={1} max={65_535} value={trafficPort} onChange={(event) => setTrafficPort(event.target.value)} /></label>
          <label>{t('Command')}<input maxLength={4_096} value={command} onChange={(event) => setCommand(event.target.value)} /></label>
          <label>{t('Entrypoint (space separated)')}<input maxLength={4_096} value={entrypoint} onChange={(event) => setEntrypoint(event.target.value)} /></label>
          <label>{t('Arguments (space separated)')}<input maxLength={4_096} value={args} onChange={(event) => setArgs(event.target.value)} /></label>
          <label>{t('Registry username')}<input maxLength={4_096} autoComplete="off" value={registryUsername} onChange={(event) => setRegistryUsername(event.target.value)} /></label>
          <label>{t('Registry secret')}<input type="password" maxLength={4_096} autoComplete="new-password" value={registrySecret} onChange={(event) => setRegistrySecret(event.target.value)} /></label>
        </div>
        <label>{t('Environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"KEY":"value"}'} value={environmentJSON} aria-invalid={error === 'configuration'} onChange={(event) => setEnvironmentJSON(event.target.value)} /></label>
        <label>{t('Secret environment variables (JSON)')}<textarea className="models-code" rows={4} maxLength={1_048_576} placeholder={'{"TOKEN":"value"}'} value={secretEnvironmentJSON} aria-invalid={error === 'configuration'} onChange={(event) => setSecretEnvironmentJSON(event.target.value)} /></label>
        {error === 'empty' && <p className="models-field-error" role="alert">{t('Enter at least one setting to update.')}</p>}
        {error === 'configuration' && <p className="models-field-error" role="alert">{t('Configuration must be valid JSON objects with string values.')}</p>}
        <p className="models-security-note">{t('Registry credentials and secret variables are sent only when you submit and are never displayed again.')}</p>
        <footer className="models-dialog-actions"><button type="button" onClick={onCancel} disabled={busy}>{t('Cancel')}</button><button type="submit" className="primary" disabled={busy}>{busy ? t('Saving…') : t('Update')}</button></footer>
      </form>
    </Overlay>
  );
}
