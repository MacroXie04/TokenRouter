import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createWaffoPancakePair,
  listWaffoPancakeCatalog,
  saveWaffoPancakeConfig,
  type WaffoPancakeCatalogStore,
} from './waffo-pancake-admin-api';

export interface WaffoPancakeOption {
  key: string;
  value: string;
}

interface WaffoPancakeAdminPanelProps {
  options: WaffoPancakeOption[];
  onSaved: () => Promise<void> | void;
}

type PanelMessage = { kind: 'success' | 'error'; text: string } | null;

function optionValue(options: WaffoPancakeOption[], key: string, fallback = ''): string {
  return options.find((option) => option.key === key)?.value ?? fallback;
}

export function WaffoPancakeAdminPanel({ options, onSaved }: WaffoPancakeAdminPanelProps) {
  const { t } = useTranslation();
  const savedMerchantId = optionValue(options, 'WaffoPancakeMerchantID').trim();
  const [merchantId, setMerchantId] = useState(savedMerchantId);
  const [privateKey, setPrivateKey] = useState('');
  const [returnUrl, setReturnUrl] = useState(optionValue(options, 'WaffoPancakeReturnURL'));
  const [unitPrice, setUnitPrice] = useState(optionValue(options, 'WaffoPancakeUnitPrice', '1'));
  const [minTopUp, setMinTopUp] = useState(optionValue(options, 'WaffoPancakeMinTopUp', '1'));
  const [storeId, setStoreId] = useState(optionValue(options, 'WaffoPancakeStoreID'));
  const [productId, setProductId] = useState(optionValue(options, 'WaffoPancakeProductID'));
  const [catalog, setCatalog] = useState<WaffoPancakeCatalogStore[]>([]);
  const [busy, setBusy] = useState('');
  const [message, setMessage] = useState<PanelMessage>(null);

  useEffect(() => {
    setMerchantId(optionValue(options, 'WaffoPancakeMerchantID'));
    setReturnUrl(optionValue(options, 'WaffoPancakeReturnURL'));
    setUnitPrice(optionValue(options, 'WaffoPancakeUnitPrice', '1'));
    setMinTopUp(optionValue(options, 'WaffoPancakeMinTopUp', '1'));
    setStoreId(optionValue(options, 'WaffoPancakeStoreID'));
    setProductId(optionValue(options, 'WaffoPancakeProductID'));
  }, [options]);

  const products = useMemo(
    () => catalog.find((store) => store.id === storeId)?.onetimeProducts ?? [],
    [catalog, storeId],
  );
  const storeChoices = useMemo(() => {
    if (storeId === '' || catalog.some((store) => store.id === storeId)) return catalog;
    return [{ id: storeId, name: storeId, status: '', prodEnabled: false, onetimeProducts: [] }, ...catalog];
  }, [catalog, storeId]);
  const productChoices = useMemo(() => {
    if (productId === '' || products.some((product) => product.id === productId)) return products;
    return [{ id: productId, name: productId, status: '' }, ...products];
  }, [productId, products]);

  function requestCredentials(): { merchantId: string; privateKey: string } | null {
    const merchant = merchantId.trim();
    const key = privateKey.trim();
    if (merchant === savedMerchantId && key === '' && savedMerchantId !== '') {
      return { merchantId: '', privateKey: '' };
    }
    if (merchant === '' || key === '') return null;
    return { merchantId: merchant, privateKey: key };
  }

  function chooseCatalog(nextCatalog: WaffoPancakeCatalogStore[], preferredStore = storeId, preferredProduct = productId) {
    setCatalog(nextCatalog);
    const preferred = nextCatalog.find((store) => store.id === preferredStore);
    if (preferred?.onetimeProducts.some((product) => product.id === preferredProduct)) return;
    const firstStore = nextCatalog.find((store) => store.onetimeProducts.length > 0);
    if (firstStore) {
      setStoreId(firstStore.id);
      setProductId(firstStore.onetimeProducts[0].id);
    }
  }

  async function verifyCredentials() {
    if (busy) return;
    const credentials = requestCredentials();
    if (!credentials) {
      setMessage({ kind: 'error', text: t('Enter both the merchant ID and private key.') });
      return;
    }
    setBusy('verify');
    setMessage(null);
    try {
      const nextCatalog = await listWaffoPancakeCatalog(credentials.merchantId, credentials.privateKey);
      chooseCatalog(nextCatalog);
      setMessage({ kind: 'success', text: t('Credentials verified and catalog loaded.') });
    } catch {
      setCatalog([]);
      setMessage({ kind: 'error', text: t('Unable to verify Waffo Pancake credentials.') });
    } finally {
      setBusy('');
    }
  }

  async function createPair() {
    if (busy) return;
    const credentials = requestCredentials();
    if (!credentials) {
      setMessage({ kind: 'error', text: t('Enter both the merchant ID and private key.') });
      return;
    }
    if (returnUrl.trim() === '' && !window.confirm(t('Create the product without a payment return URL?'))) return;
    setBusy('pair');
    setMessage(null);
    try {
      const pair = await createWaffoPancakePair({ ...credentials, returnUrl });
      setStoreId(pair.storeId);
      setProductId(pair.productId);
      try {
        const nextCatalog = await listWaffoPancakeCatalog(credentials.merchantId, credentials.privateKey);
        chooseCatalog(nextCatalog, pair.storeId, pair.productId);
      } catch {
        setCatalog([]);
      }
      setMessage({ kind: 'success', text: t('Waffo Pancake store and product created.') });
    } catch {
      setMessage({ kind: 'error', text: t('Unable to create the Waffo Pancake store and product.') });
    } finally {
      setBusy('');
    }
  }

  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    const merchant = merchantId.trim();
    if (merchant === '' || storeId === '' || productId === '') {
      setMessage({ kind: 'error', text: t('Choose a merchant, store, and wallet product before saving.') });
      return;
    }
    if (merchant !== savedMerchantId && privateKey.trim() === '') {
      setMessage({ kind: 'error', text: t('Enter both the merchant ID and private key.') });
      return;
    }
    setBusy('save');
    setMessage(null);
    try {
      const binding = await saveWaffoPancakeConfig({
        merchantId: merchant,
        privateKey,
        returnUrl,
        storeId,
        productId,
        unitPrice,
        minTopUp,
      });
      setStoreId(binding.storeId);
      setProductId(binding.productId);
      setPrivateKey('');
      setMessage({ kind: 'success', text: t('Waffo Pancake settings saved.') });
      await onSaved();
    } catch {
      setMessage({ kind: 'error', text: t('Unable to save Waffo Pancake settings.') });
    } finally {
      setBusy('');
    }
  }

  const serverAddress = optionValue(options, 'ServerAddress', '<ServerAddress>').replace(/\/$/, '');

  return (
    <section className="settings-panel" aria-labelledby="waffo-pancake-settings-title">
      <h3 id="waffo-pancake-settings-title">{t('Waffo Pancake')}</h3>
      <p className="muted">{t('Configure the hosted checkout, wallet product, and signed webhook endpoints.')}</p>
      <p className="muted">
        {t('Webhook (test)')}: <code>{serverAddress}/api/waffo-pancake/webhook/test</code><br />
        {t('Webhook (production)')}: <code>{serverAddress}/api/waffo-pancake/webhook/prod</code>
      </p>
      <form className="grid-form" onSubmit={save}>
        <label>
          {t('Merchant ID')}
          <input value={merchantId} maxLength={64} autoComplete="off" placeholder="MER_…" onChange={(event) => setMerchantId(event.target.value)} />
        </label>
        <label>
          {t('API private key')}
          <textarea
            value={privateKey}
            rows={4}
            maxLength={16 * 1024}
            autoComplete="new-password"
            placeholder={t('Leave blank to keep the existing private key')}
            onChange={(event) => setPrivateKey(event.target.value)}
          />
        </label>
        <label>
          {t('Payment return URL')}
          <input value={returnUrl} maxLength={2_048} placeholder="https://example.com/wallet" onChange={(event) => setReturnUrl(event.target.value)} />
        </label>
        <label>
          {t('Unit price (USD)')}
          <input value={unitPrice} maxLength={64} inputMode="decimal" onChange={(event) => setUnitPrice(event.target.value)} />
        </label>
        <label>
          {t('Minimum top-up')}
          <input value={minTopUp} maxLength={32} inputMode="numeric" onChange={(event) => setMinTopUp(event.target.value)} />
        </label>
        <div className="inline-form">
          <button type="button" disabled={busy !== ''} onClick={() => void verifyCredentials()}>
            {busy === 'verify' ? t('Verifying…') : t('Verify and load catalog')}
          </button>
          <button type="button" disabled={busy !== ''} onClick={() => void createPair()}>
            {busy === 'pair' ? t('Creating…') : t('Create store and wallet product')}
          </button>
        </div>
        <label>
          {t('Pancake store')}
          <select value={storeId} onChange={(event) => {
            const nextStore = event.target.value;
            const nextProducts = catalog.find((store) => store.id === nextStore)?.onetimeProducts ?? [];
            setStoreId(nextStore);
            setProductId(nextProducts[0]?.id ?? '');
          }}>
            <option value="">{t('Select a store')}</option>
            {storeChoices.map((store) => <option key={store.id} value={store.id}>{store.name} ({store.id})</option>)}
          </select>
        </label>
        <label>
          {t('Wallet product')}
          <select value={productId} disabled={storeId === ''} onChange={(event) => setProductId(event.target.value)}>
            <option value="">{t('Select a product')}</option>
            {productChoices.map((product) => <option key={product.id} value={product.id}>{product.name} ({product.id})</option>)}
          </select>
        </label>
        <button type="submit" disabled={busy !== ''}>{busy === 'save' ? t('Saving…') : t('Save Waffo Pancake settings')}</button>
      </form>
      {message && <p role="status" className={message.kind}>{message.text}</p>}
    </section>
  );
}
