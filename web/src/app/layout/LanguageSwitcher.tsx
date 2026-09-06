import { useTranslation } from 'react-i18next';
import { supportedLanguages } from '../../i18n';

export function LanguageSwitcher() {
  const { t, i18n } = useTranslation();
  return (
    <select
      className="lang"
      value={i18n.language}
      onChange={(event) => i18n.changeLanguage(event.target.value)}
      aria-label={t('Language')}
    >
      {supportedLanguages.map((language) => (
        <option key={language.code} value={language.code}>{language.label}</option>
      ))}
    </select>
  );
}
