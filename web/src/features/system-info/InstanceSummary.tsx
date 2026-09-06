import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getData } from '../../shared/api/client';

interface Instance {
  node_name: string;
  started_at: number;
  last_seen_at: number;
}

/** The administrator dashboard retains its existing, read-only node summary. */
export function InstanceSummary() {
  const { t } = useTranslation();
  const [instances, setInstances] = useState<Instance[]>([]);
  useEffect(() => {
    let active = true;
    void getData<Instance[]>('/instance').then((value) => {
      if (active) setInstances(value);
    }).catch(() => undefined);
    return () => { active = false; };
  }, []);
  if (instances.length === 0) return null;
  return (
    <section className="card">
      <h2>{t('Nodes')}</h2>
      <ul className="key-list">
        {instances.map((instance) => (
          <li key={instance.node_name}>
            <strong>{instance.node_name}</strong>
            <span className="muted">{t('Seen {{time}}', { time: new Date(instance.last_seen_at * 1000).toLocaleString() })}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
