import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { HeaderNavigationModules } from '../../shared/config/nav-modules';
import type { PublicAnnouncementInfo } from '../../shared/config/public-status';
import { SystemBrand, useSystemBrand } from '../../shared/ui/SystemBrand';
import type { PricingCatalog } from '../pricing/catalog';
import { loadPricingCatalog } from '../pricing/pricing-api';
import { documentFormat, externalDocumentURL } from '../public-documents/content';
import type { RankingRow } from '../rankings/rankings';
import {
  loadBasicRankings,
  loadPerformanceSummary,
  loadPublicContent,
  MAX_PUBLIC_CONTENT_CHARACTERS,
  type PerformanceSummary,
} from './home-api';
import './home.css';
import { PublicNotice, SafePublicMarkdown } from './PublicNotice';

type RequestState = 'loading' | 'ready' | 'error';

const HOME_CONTENT_CACHE_KEY = 'tokenrouter.home_page_content.v1';

interface HomeViewProps {
  onSignIn: () => void;
  signedIn: boolean;
  modules: HeaderNavigationModules;
  announcementsEnabled?: boolean;
  announcements?: readonly PublicAnnouncementInfo[];
}

interface HomeEndpointApplication {
  type: string;
  method: string;
  path: string;
  modelName: string;
}

function readCachedHomeContent(): string {
  try {
    const cached = window.localStorage?.getItem(HOME_CONTENT_CACHE_KEY);
    if (!cached) return '';
    if (cached.length > MAX_PUBLIC_CONTENT_CHARACTERS || documentFormat(cached) === 'too-large') {
      window.localStorage?.removeItem(HOME_CONTENT_CACHE_KEY);
      return '';
    }
    return cached.trim();
  } catch {
    return '';
  }
}

function writeCachedHomeContent(content: string): void {
  try {
    if (content) window.localStorage?.setItem(HOME_CONTENT_CACHE_KEY, content);
    else window.localStorage?.removeItem(HOME_CONTENT_CACHE_KEY);
  } catch {
    // Storage only prevents a blank custom page while refreshing.
  }
}

function preferredColorScheme(): 'light' | 'dark' {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return 'dark';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function safeFrameLanguage(value: string): string {
  const language = value.trim();
  return /^[A-Za-z0-9-]{1,32}$/.test(language) ? language : 'en';
}

function ExternalHomeFrame({ url, title, language }: { url: string; title: string; language: string }) {
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [themeMode, setThemeMode] = useState<'light' | 'dark'>(preferredColorScheme);
  const targetOrigin = useMemo(() => new URL(url).origin, [url]);
  const normalizedLanguage = safeFrameLanguage(language);

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return undefined;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const changed = (event: MediaQueryListEvent) => setThemeMode(event.matches ? 'dark' : 'light');
    media.addEventListener?.('change', changed);
    return () => media.removeEventListener?.('change', changed);
  }, []);

  const synchronize = useCallback(() => {
    const target = frameRef.current?.contentWindow;
    if (!target) return;
    try {
      target.postMessage({ themeMode }, targetOrigin);
      target.postMessage({ lang: normalizedLanguage }, targetOrigin);
    } catch {
      // The constrained frame stays usable if it navigates while preferences are sent.
    }
  }, [normalizedLanguage, targetOrigin, themeMode]);

  useEffect(() => synchronize(), [synchronize]);

  return (
    <iframe
      className="document-frame home-custom-frame"
      onLoad={synchronize}
      ref={frameRef}
      referrerPolicy="no-referrer"
      sandbox="allow-forms allow-popups allow-popups-to-escape-sandbox allow-scripts allow-top-navigation-by-user-activation"
      src={url}
      title={title}
    />
  );
}

function ConfiguredHomeContent({ content, title, language }: { content: string; title: string; language: string }) {
  const format = documentFormat(content);
  const externalURL = format === 'external-url' ? externalDocumentURL(content) : null;
  if (externalURL) {
    return <ExternalHomeFrame language={language} title={title} url={externalURL} />;
  }
  if (format === 'html') {
    return (
      <iframe
        className="document-frame home-custom-frame"
        referrerPolicy="no-referrer"
        sandbox=""
        srcDoc={content}
        title={title}
      />
    );
  }
  return (
    <div className="document-markdown home-custom-content">
      <SafePublicMarkdown content={content} />
    </div>
  );
}

function catalogEndpointApplications(catalog: PricingCatalog | null): HomeEndpointApplication[] {
  if (!catalog) return [];
  const items = catalog.items.slice().sort((left, right) => left.model_name.localeCompare(right.model_name));
  return Object.keys(catalog.supportedEndpoint).sort().flatMap((type) => {
    const item = items.find((candidate) => candidate.supported_endpoint_types.includes(type));
    if (!item) return [];
    const endpoint = catalog.supportedEndpoint[type];
    return [{
      type,
      method: endpoint.method,
      path: endpoint.path.replaceAll('{model}', item.model_name),
      modelName: item.model_name,
    }];
  }).slice(0, 6);
}

function stateValue(state: RequestState, value: number): string {
  if (state === 'loading') return '…';
  if (state === 'error') return '—';
  return String(value);
}

function formatPrice(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 6 }).format(value);
}

export function HomeView({
  onSignIn,
  signedIn,
  modules,
  announcementsEnabled = false,
  announcements = [],
}: HomeViewProps) {
  const { t, i18n } = useTranslation();
  const { systemName } = useSystemBrand();
  const pricingEnabled = modules.pricing.enabled;
  const rankingsEnabled = modules.rankings.enabled;
  const [initialHomeContent] = useState(readCachedHomeContent);
  const [homeContent, setHomeContent] = useState(initialHomeContent);
  const [homeContentState, setHomeContentState] = useState<RequestState>('loading');
  const [catalog, setCatalog] = useState<PricingCatalog | null>(null);
  const [pricingState, setPricingState] = useState<RequestState>('loading');
  const [rankings, setRankings] = useState<RankingRow[]>([]);
  const [rankingState, setRankingState] = useState<RequestState>('loading');
  const [performance, setPerformance] = useState<PerformanceSummary[]>([]);
  const [performanceState, setPerformanceState] = useState<RequestState>('loading');
  const [about, setAbout] = useState('');
  const [aboutState, setAboutState] = useState<RequestState>('loading');
  const [agreement, setAgreement] = useState('');
  const [agreementState, setAgreementState] = useState<RequestState>('loading');
  const [privacy, setPrivacy] = useState('');
  const [privacyState, setPrivacyState] = useState<RequestState>('loading');

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    const failed = (setState: (state: RequestState) => void) => {
      if (active && !controller.signal.aborted) setState('error');
    };

    loadPublicContent('/home_page_content', controller.signal)
      .then((content) => {
        if (!active) return;
        setHomeContent(content);
        writeCachedHomeContent(content);
        setHomeContentState('ready');
      })
      .catch(() => {
        if (!active || controller.signal.aborted) return;
        setHomeContentState(initialHomeContent ? 'ready' : 'error');
      });
    if (pricingEnabled) {
      loadPricingCatalog(controller.signal)
        .then((value) => {
          if (!active) return;
          setCatalog(value);
          setPricingState('ready');
        })
        .catch(() => failed(setPricingState));
      loadPerformanceSummary(controller.signal)
        .then((value) => {
          if (!active) return;
          setPerformance(value);
          setPerformanceState('ready');
        })
        .catch(() => failed(setPerformanceState));
    } else {
      setPricingState('ready');
      setPerformanceState('ready');
    }
    if (rankingsEnabled) {
      loadBasicRankings(controller.signal)
        .then((value) => {
          if (!active) return;
          setRankings(value);
          setRankingState('ready');
        })
        .catch(() => failed(setRankingState));
    } else {
      setRankingState('ready');
    }
    loadPublicContent('/about', controller.signal)
      .then((value) => {
        if (!active) return;
        setAbout(value);
        setAboutState('ready');
      })
      .catch(() => failed(setAboutState));
    loadPublicContent('/user-agreement', controller.signal)
      .then((value) => {
        if (!active) return;
        setAgreement(value);
        setAgreementState('ready');
      })
      .catch(() => failed(setAgreementState));
    loadPublicContent('/privacy-policy', controller.signal)
      .then((value) => {
        if (!active) return;
        setPrivacy(value);
        setPrivacyState('ready');
      })
      .catch(() => failed(setPrivacyState));

    return () => {
      active = false;
      controller.abort();
    };
  }, [initialHomeContent, pricingEnabled, rankingsEnabled]);

  const previewPrices = useMemo(
    () => (catalog?.items ?? []).slice().sort((left, right) => left.model_name.localeCompare(right.model_name)).slice(0, 6),
    [catalog],
  );
  const topRanks = useMemo(
    () => rankings.slice().sort((left, right) => right.count - left.count || left.model_name.localeCompare(right.model_name)).slice(0, 5),
    [rankings],
  );
  const performanceLeaders = useMemo(
    () => performance.slice().sort((left, right) => right.success_rate - left.success_rate || left.model_name.localeCompare(right.model_name)).slice(0, 5),
    [performance],
  );
  const endpointApplications = useMemo(() => catalogEndpointApplications(catalog), [catalog]);
  const terminalApplication = endpointApplications[0];
  const legalLoading = agreementState === 'loading' || privacyState === 'loading';
  const legalFailed = agreementState === 'error' || privacyState === 'error';
  const legalEmpty = agreementState === 'ready' && privacyState === 'ready' && !agreement && !privacy;

  const header = (
    <header className="home-header">
      <SystemBrand variant="header" />
      <nav className="home-actions" aria-label={t('Primary navigation')}>
        {modules.home !== false && <a href="/" aria-current="page">{t('Home')}</a>}
        {modules.console !== false && <a href="/dashboard">{t('Console')}</a>}
        {pricingEnabled && <a href="/pricing">{t('Pricing')}</a>}
        {rankingsEnabled && <a href="/rankings">{t('Rankings')}</a>}
        {modules.docs !== false && <a href="#api-quickstart">{t('Documentation')}</a>}
        {modules.about !== false && <a href="/about">{t('About')}</a>}
        {!signedIn && <button className="link" type="button" onClick={onSignIn}>{t('Sign in')}</button>}
      </nav>
    </header>
  );

  if (homeContent) {
    return (
      <main className="app home-page home-custom-app" aria-busy={homeContentState === 'loading'}>
        {header}
        <PublicNotice announcements={announcements} announcementsEnabled={announcementsEnabled} />
        <section className="card home-custom" aria-label={t('Custom home page')}>
          <ConfiguredHomeContent
            content={homeContent}
            language={i18n.resolvedLanguage || i18n.language}
            title={t('Custom home page')}
          />
        </section>
      </main>
    );
  }

  return (
    <main className="app home-page" aria-busy={homeContentState === 'loading'}>
      {header}
      <PublicNotice announcements={announcements} announcementsEnabled={announcementsEnabled} />
      {homeContentState === 'error' && (
        <section className="error-panel home-content-error" role="alert">{t('Unable to load custom home content.')}</section>
      )}

      <section className="home-hero" aria-labelledby="home-hero-heading">
        <div>
          <p className="home-eyebrow">{t('Unified AI API gateway')}</p>
          <h1 id="home-hero-heading">{t('One gateway for every AI workflow')}</h1>
          <p className="home-hero-copy">
            {t('Connect your applications to the models and API routes published by this gateway, with pricing and performance in one place.')}
          </p>
          <div className="home-hero-actions">
            {signedIn
              ? modules.console !== false && <button type="button" onClick={onSignIn}>{t('Open dashboard')}</button>
              : <a className="button" href="/sign-up">{t('Create an account.')}</a>}
            {pricingEnabled && <a className="button" href="/pricing">{t('Explore pricing')}</a>}
            {modules.docs !== false && <a className="button" href="#api-quickstart">{t('Documentation')}</a>}
          </div>
        </div>
        <div className="home-route-card" aria-label={t('Gateway request flow')} role="img">
          <span>{t('Your application')}</span><strong aria-hidden="true">→</strong>
          <span>{systemName}</span><strong aria-hidden="true">→</strong>
          <span>{t('Available model')}</span>
        </div>
      </section>

      {modules.docs !== false && (
        <section className="home-section home-api-showcase" id="api-quickstart" aria-labelledby="home-api-heading" aria-busy={pricingState === 'loading'}>
          <p className="home-eyebrow">{t('Documentation')}</p>
          <h2 id="home-api-heading">{t('Gateway API')}</h2>
          {pricingState === 'loading' && <p className="muted" role="status">{t('Loading pricing…')}</p>}
          {pricingState === 'error' && <p className="error" role="alert">{t('Unable to load pricing summary.')}</p>}
          {pricingState === 'ready' && !terminalApplication && <p className="muted">{t('No API routes are advertised.')}</p>}
          {pricingState === 'ready' && terminalApplication && (
            <div className="home-api-grid">
              <figure className="home-api-terminal">
                <figcaption>{t('API endpoint')}</figcaption>
                <pre><code>{`${terminalApplication.method} ${terminalApplication.path}\nmodel: ${terminalApplication.modelName}`}</code></pre>
              </figure>
              <nav className="home-endpoint-applications" aria-label={t('Supported API routes')}>
                {endpointApplications.map((application) => (
                  <a
                    aria-label={`${application.type} ${application.method} ${application.path}`}
                    href={`/pricing?endpointType=${encodeURIComponent(application.type)}`}
                    key={application.type}
                  >
                    <strong>{application.type}</strong>
                    <span><code>{application.method}</code> {application.path}</span>
                  </a>
                ))}
              </nav>
            </div>
          )}
        </section>
      )}

      {pricingEnabled && (
        <section className="home-stats" aria-label={t('Gateway catalog statistics')} aria-busy={pricingState === 'loading' || performanceState === 'loading'}>
          <article><strong>{stateValue(pricingState, catalog?.items.length ?? 0)}</strong><span>{t('available models')}</span></article>
          <article><strong>{stateValue(pricingState, Object.keys(catalog?.supportedEndpoint ?? {}).length)}</strong><span>{t('API routes')}</span></article>
          <article><strong>{stateValue(pricingState, Object.keys(catalog?.usableGroup ?? {}).length)}</strong><span>{t('access groups')}</span></article>
          <article><strong>{stateValue(performanceState, performance.length)}</strong><span>{t('models measured in 24h')}</span></article>
        </section>
      )}

      <section className="home-section" aria-labelledby="home-features-heading">
        <p className="home-eyebrow">{t('Core capabilities')}</p>
        <h2 id="home-features-heading">{t('Built around what this gateway publishes')}</h2>
        <div className="home-feature-grid">
          <article className="card">
            <span className="home-feature-number">01</span><h3>{t('Unified model access')}</h3>
            <p>{t('Browse the live model catalog and each supported API route before you integrate.')}</p>
          </article>
          <article className="card">
            <span className="home-feature-number">02</span><h3>{t('Transparent pricing')}</h3>
            <p>{t('Compare published input and output prices, owners, and access-group adjustments.')}</p>
          </article>
          <article className="card">
            <span className="home-feature-number">03</span><h3>{t('Measured performance')}</h3>
            <p>{t('Review live latency, throughput, success rates, and usage rankings when data is available.')}</p>
          </article>
        </div>
      </section>

      <section className="home-section" aria-labelledby="home-how-heading">
        <p className="home-eyebrow">{t('How it works')}</p>
        <h2 id="home-how-heading">{t('Three steps from catalog to request')}</h2>
        <ol className="home-steps">
          <li><span>1</span><div><h3>{t('Choose')}</h3><p>{t('Find a model, owner, modality, and access group that fit your workload.')}</p></div></li>
          <li><span>2</span><div><h3>{t('Connect')}</h3><p>{t('Use one of the API routes explicitly advertised for that model.')}</p></div></li>
          <li><span>3</span><div><h3>{t('Observe')}</h3><p>{t('Follow published usage and performance data as requests flow through the gateway.')}</p></div></li>
        </ol>
      </section>

      {(pricingEnabled || rankingsEnabled) && <section className="home-live-grid" aria-label={t('Live gateway data')}>
        {pricingEnabled && <article className="card" aria-labelledby="home-pricing-heading" aria-busy={pricingState === 'loading'}>
          <header><h2 id="home-pricing-heading">{t('Published pricing')}</h2><a href="/pricing">{t('View all')}</a></header>
          {pricingState === 'loading' && <p className="muted" role="status">{t('Loading pricing…')}</p>}
          {pricingState === 'error' && <p className="error" role="alert">{t('Unable to load pricing summary.')}</p>}
          {pricingState === 'ready' && previewPrices.length === 0 && <p className="muted">{t('No custom model prices are published.')}</p>}
          {pricingState === 'ready' && previewPrices.length > 0 && (
            <ul className="home-data-list">{previewPrices.map((item) => (
              <li key={item.model_name}>
                <a href={`/pricing/${encodeURIComponent(item.model_name)}`}>{item.model_name}</a>
                <span>{t('${{input}} input · ${{output}} output / 1M', {
                  input: formatPrice(item.prompt_price), output: formatPrice(item.completion_price),
                })}</span>
              </li>
            ))}</ul>
          )}
        </article>}

        {pricingEnabled && <article className="card" aria-labelledby="home-performance-heading" aria-busy={performanceState === 'loading'}>
          <h2 id="home-performance-heading">{t('Model performance (24h)')}</h2>
          {performanceState === 'loading' && <p className="muted" role="status">{t('Loading model performance…')}</p>}
          {performanceState === 'error' && <p className="error" role="alert">{t('Unable to load model performance.')}</p>}
          {performanceState === 'ready' && performanceLeaders.length === 0 && <p className="muted">{t('No performance data yet.')}</p>}
          {performanceState === 'ready' && performanceLeaders.length > 0 && (
            <ul className="home-data-list">{performanceLeaders.map((metric) => (
              <li key={metric.model_name}><strong>{metric.model_name}</strong><span>{t('{{latency}} ms · {{rate}}% · {{tps}} tokens/s', {
                latency: metric.avg_latency_ms, rate: metric.success_rate.toFixed(2), tps: metric.avg_tps.toFixed(2),
              })}</span></li>
            ))}</ul>
          )}
        </article>}

        {rankingsEnabled && <article className="card" aria-labelledby="home-rankings-heading" aria-busy={rankingState === 'loading'}>
          <header><h2 id="home-rankings-heading">{t('Top models')}</h2><a href="/rankings">{t('View rankings')}</a></header>
          {rankingState === 'loading' && <p className="muted" role="status">{t('Loading top models…')}</p>}
          {rankingState === 'error' && <p className="error" role="alert">{t('Unable to load top models.')}</p>}
          {rankingState === 'ready' && topRanks.length === 0 && <p className="muted">{t('No usage yet.')}</p>}
          {rankingState === 'ready' && topRanks.length > 0 && (
            <ol className="home-data-list">{topRanks.map((rank) => (
              <li key={rank.model_name}><strong>{rank.model_name}</strong><span>{t('{{count}} requests · {{quota}} quota', { count: rank.count, quota: rank.quota })}</span></li>
            ))}</ol>
          )}
        </article>}
      </section>}

      <section className="home-cta" aria-labelledby="home-cta-heading">
        <div><h2 id="home-cta-heading">{t('Ready to route your next request?')}</h2><p>{t('Start with the live catalog, then sign in when you are ready to connect.')}</p></div>
        <div className="home-hero-actions">
          {signedIn
            ? modules.console !== false && <button type="button" onClick={onSignIn}>{t('Open dashboard')}</button>
            : <a className="button" href="/sign-up">{t('Create an account.')}</a>}
          {pricingEnabled && <a className="button" href="/pricing">{t('View pricing')}</a>}
          {modules.docs !== false && <a className="button" href="#api-quickstart">{t('Documentation')}</a>}
        </div>
      </section>

      <footer className="footer" aria-label={t('About TokenRouter')} aria-busy={aboutState === 'loading' || legalLoading}>
        {aboutState === 'loading' && <p className="muted" role="status">{t('Loading About information…')}</p>}
        {aboutState === 'error' && <p className="error" role="alert">{t('Unable to load About information.')}</p>}
        {aboutState === 'ready' && <p>{about || `${systemName} — ${t('independent AI API gateway.')}`}</p>}
        <nav className="legal" aria-label={t('Legal information')}>
          {legalLoading && <span className="muted" role="status">{t('Checking legal documents…')}</span>}
          {legalFailed && <span className="error" role="alert">{t('Unable to check all legal documents.')}</span>}
          {agreement && <a href="/user-agreement">{t('User Agreement')}</a>}
          {privacy && <a href="/privacy-policy">{t('Privacy Policy')}</a>}
          {legalEmpty && <span className="muted">{t('No legal documents are currently published.')}</span>}
          {modules.about !== false && <a href="/about">{t('About')}</a>}
        </nav>
      </footer>
    </main>
  );
}
