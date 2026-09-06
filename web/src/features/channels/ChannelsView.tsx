import { useState, type ComponentProps } from 'react';
import { useTranslation } from 'react-i18next';
import { ChannelAdminView } from './ChannelAdminView';
import { RoutingAbilitiesPanel } from './RoutingAbilitiesPanel';
import { PrefillGroupPanel } from './PrefillGroupPanel';

export function ChannelsView(props: ComponentProps<typeof ChannelAdminView>) {
  const { t } = useTranslation();
  const [section, setSection] = useState<'channels' | 'abilities' | 'prefill'>('channels');
  return (
    <main className="app">
      <nav className="tabs" aria-label={t('Channels')}>
        {props.canRead && <button className={section === 'channels' ? 'tab active' : 'tab'} onClick={() => setSection('channels')}>{t('Channels')}</button>}
        {props.canRead && <button className={section === 'abilities' ? 'tab active' : 'tab'} onClick={() => setSection('abilities')}>{t('Abilities')}</button>}
        <button className={section === 'prefill' ? 'tab active' : 'tab'} onClick={() => setSection('prefill')}>{t('Prefill')}</button>
      </nav>
      {(section === 'channels' || (section === 'abilities' && !props.canRead)) && <ChannelAdminView {...props} />}
      {section === 'abilities' && props.canRead && <RoutingAbilitiesPanel canWrite={props.canWrite} />}
      {section === 'prefill' && <PrefillGroupPanel />}
    </main>
  );
}
