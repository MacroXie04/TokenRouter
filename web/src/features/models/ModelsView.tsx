import { useTranslation } from 'react-i18next';
import { DeploymentSection } from './ModelDeploymentsSection';
import { MetadataSection } from './ModelMetadataSection';
import { MODEL_SECTIONS, type ModelsSection } from "./metadata-contracts";
import { assertModelsAdministrator } from "./model-response";
import './models.css';

export interface ModelsViewProps {
  section: ModelsSection;
  role: number;
  onNavigate?: (target: string) => void;
}

export function ModelsView({ section, role, onNavigate }: ModelsViewProps) {
  const { t } = useTranslation();
  try {
    assertModelsAdministrator(role);
  } catch {
    return <section className="models-access-state" role="alert"><h1>{t('Administrator access required')}</h1><p>{t('You do not have permission to manage model metadata or deployments.')}</p></section>;
  }
  const activeSection = MODEL_SECTIONS.includes(section) ? section : 'metadata';
  return (
    <main className="models-view">
      <nav className="models-tabs" aria-label={t('Model management sections')}>
        {MODEL_SECTIONS.map((value) => <button type="button" key={value} aria-current={activeSection === value ? 'page' : undefined} className={activeSection === value ? 'active' : ''} onClick={() => { if (value !== activeSection) onNavigate?.(`/models/${value}`); }}>{value === 'metadata' ? t('Metadata') : t('Deployments')}</button>)}
      </nav>
      {activeSection === 'metadata' ? <MetadataSection /> : <DeploymentSection />}
    </main>
  );
}
