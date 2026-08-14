import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getData } from '../api';

interface Rank {
  model_name: string;
  count: number;
  quota: number;
}

interface Price {
  prompt: number;
  completion: number;
}

export function HomeView({ onSignIn }: { onSignIn: () => void }) {
  const { t } = useTranslation();
  const [rankings, setRankings] = useState<Rank[]>([]);
  const [prices, setPrices] = useState<Record<string, Price>>({});
  const [about, setAbout] = useState('');
  const [agreement, setAgreement] = useState('');
  const [privacy, setPrivacy] = useState('');

  useEffect(() => {
    getData<Rank[]>('/rankings').then(setRankings).catch(() => {});
    getData<{ model_prices: Record<string, Price> }>('/ratio_config')
      .then((r) => setPrices(r.model_prices ?? {}))
      .catch(() => {});
    getData<{ about?: string }>('/about').then((r) => setAbout(r.about ?? '')).catch(() => {});
    getData<string>('/user-agreement').then(setAgreement).catch(() => {});
    getData<string>('/privacy-policy').then(setPrivacy).catch(() => {});
  }, []);

  return (
    <main className="app">
      <header className="header row">
        <div>
          <h1>TokenRouter</h1>
          <p className="tagline">A production-grade AI API gateway.</p>
        </div>
        <div>
          <button className="link" onClick={onSignIn}>{t('Sign in')}</button>
        </div>
      </header>

      <section className="card">
        <h2>Pricing</h2>
        {Object.keys(prices).length === 0 ? (
          <p className="muted">Default pricing ($1.00 / 1M input, $3.00 / 1M output).</p>
        ) : (
          <ul className="key-list">
            {Object.entries(prices).map(([model, p]) => (
              <li key={model}>
                <strong>{model}</strong>
                <span className="muted">${p.prompt} / 1M input</span>
                <span className="muted">${p.completion} / 1M output</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="card">
        <h2>Top models</h2>
        {rankings.length === 0 ? (
          <p className="muted">No usage yet.</p>
        ) : (
          <ul className="key-list">
            {rankings.slice(0, 20).map((r) => (
              <li key={r.model_name}>
                <strong>{r.model_name}</strong>
                <span className="muted">{r.count} requests</span>
                <span className="muted">{r.quota} quota</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <footer className="footer">
        {about ? <p>{about}</p> : <p>TokenRouter — independent AI API gateway. MIT licensed.</p>}
        {(agreement || privacy) && (
          <div className="legal">
            {agreement && <a href="#" onClick={(e) => { e.preventDefault(); alert(agreement); }}>User Agreement</a>}
            {privacy && <a href="#" onClick={(e) => { e.preventDefault(); alert(privacy); }}>Privacy Policy</a>}
          </div>
        )}
      </footer>
    </main>
  );
}
