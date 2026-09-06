import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { getData, postData } from '../../shared/api/client';

interface RoutingAbility {
  group: string;
  model: string;
  channel_id: number;
  enabled: boolean;
  weight: number;
}

export function RoutingAbilitiesPanel({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const [abilities, setAbilities] = useState<RoutingAbility[]>([]);
  const [group, setGroup] = useState('default');
  const [model, setModel] = useState('');
  const [channel, setChannel] = useState(0);
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);

  useEffect(() => {
    let active = true;
    void getData<RoutingAbility[]>('/ability').then((value) => {
      if (active) setAbilities(value);
    }).catch(() => { if (active) setError(true); });
    return () => { active = false; };
  }, [reload]);

  async function createAbility(event: FormEvent) {
    event.preventDefault();
    if (!canWrite || busy) return;
    setBusy(true);
    setError(false);
    try {
      await postData('/ability', { group, model, channel_id: channel, weight: 1 });
      setModel('');
      setReload((value) => value + 1);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card">
      <h2>{t('Routing abilities')}</h2>
      {error && <p className="error" role="alert">{t('Request failed.')}</p>}
      {canWrite && <form className="grid-form" onSubmit={createAbility}>
        <label>{t('Group')}<input value={group} onChange={(event) => setGroup(event.target.value)} /></label>
        <label>{t('Model')}<input value={model} onChange={(event) => setModel(event.target.value)} required /></label>
        <label>{t('Channel ID')}<input type="number" value={channel} onChange={(event) => setChannel(Number(event.target.value))} required /></label>
        <button type="submit" disabled={busy}>{t('Add ability')}</button>
      </form>}
      <ul className="key-list">
        {abilities.map((ability) => (
          <li key={`${ability.group}:${ability.model}:${ability.channel_id}`}>
            <strong>{ability.group} / {ability.model}</strong>
            <span className="muted">{t('Channel {{id}}', { id: ability.channel_id })}</span>
            <span className="muted">{t('Weight {{weight}}', { weight: ability.weight })}</span>
            <span className="muted">{ability.enabled ? t('Enabled') : t('Disabled')}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
