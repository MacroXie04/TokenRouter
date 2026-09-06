// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createWaffoPancakePair,
  listWaffoPancakeCatalog,
  saveWaffoPancakeConfig,
} from './waffo-pancake-admin-api';
import { WaffoPancakeAdminPanel, type WaffoPancakeOption } from './WaffoPancakeAdminPanel';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('./waffo-pancake-admin-api', () => ({
  createWaffoPancakePair: vi.fn(),
  listWaffoPancakeCatalog: vi.fn(),
  saveWaffoPancakeConfig: vi.fn(),
}));

const mockedCatalog = vi.mocked(listWaffoPancakeCatalog);
const mockedPair = vi.mocked(createWaffoPancakePair);
const mockedSave = vi.mocked(saveWaffoPancakeConfig);
const merchantId = 'MER_AbCdEfGhIjKlMnOpQrStUv';
const storeId = 'STO_AbCdEfGhIjKlMnOpQrStUv';
const productId = 'PROD_AbCdEfGhIjKlMnOpQrStUv';

const options: WaffoPancakeOption[] = [
  { key: 'ServerAddress', value: 'https://router.example.test/' },
  { key: 'WaffoPancakeMerchantID', value: merchantId },
  { key: 'WaffoPancakeReturnURL', value: 'https://router.example.test/wallet' },
  { key: 'WaffoPancakeUnitPrice', value: '2.5' },
  { key: 'WaffoPancakeMinTopUp', value: '30' },
  { key: 'WaffoPancakeStoreID', value: storeId },
  { key: 'WaffoPancakeProductID', value: productId },
];

const catalog = [{
  id: storeId,
  name: 'Primary store',
  status: 'active',
  prodEnabled: true,
  onetimeProducts: [{ id: productId, name: 'Wallet product', status: 'active' }],
}];

beforeEach(() => {
  vi.resetAllMocks();
  mockedCatalog.mockResolvedValue(catalog);
  mockedPair.mockResolvedValue({ storeId, storeName: 'Primary store', productId, productName: 'Wallet product' });
  mockedSave.mockResolvedValue({ storeId, productId });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('WaffoPancakeAdminPanel', () => {
  it('uses persisted credentials without re-exposing the private key and saves all settings atomically', async () => {
    const onSaved = vi.fn();
    render(<WaffoPancakeAdminPanel options={options} onSaved={onSaved} />);
    const user = userEvent.setup();

    expect(screen.getByText('https://router.example.test/api/waffo-pancake/webhook/test')).toBeTruthy();
    expect((screen.getByLabelText('API private key') as HTMLTextAreaElement).value).toBe('');
    await user.click(screen.getByRole('button', { name: 'Verify and load catalog' }));
    expect(mockedCatalog).toHaveBeenCalledWith('', '');
    expect(await screen.findByText('Credentials verified and catalog loaded.')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Save Waffo Pancake settings' }));
    expect(mockedSave).toHaveBeenCalledWith({
      merchantId,
      privateKey: '',
      returnUrl: 'https://router.example.test/wallet',
      storeId,
      productId,
      unitPrice: '2.5',
      minTopUp: '30',
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('Waffo Pancake settings saved.')).toBeTruthy();
  });

  it('requires a complete changed credential pair and sends newly typed credentials only for verification', async () => {
    render(<WaffoPancakeAdminPanel options={options} onSaved={vi.fn()} />);
    const user = userEvent.setup();
    const merchant = screen.getByLabelText('Merchant ID');
    await user.clear(merchant);
    await user.type(merchant, 'MER_ZyXwVuTsRqPoNmLkJiHgFe');
    await user.click(screen.getByRole('button', { name: 'Verify and load catalog' }));
    expect(await screen.findByText('Enter both the merchant ID and private key.')).toBeTruthy();
    expect(mockedCatalog).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText('API private key'), 'new-private-key');
    await user.click(screen.getByRole('button', { name: 'Verify and load catalog' }));
    expect(mockedCatalog).toHaveBeenCalledWith('MER_ZyXwVuTsRqPoNmLkJiHgFe', 'new-private-key');
  });

  it('creates a pair, refreshes the authoritative catalog, and never renders provider error details', async () => {
    const { rerender } = render(<WaffoPancakeAdminPanel options={options} onSaved={vi.fn()} />);
    const user = userEvent.setup();
    await user.clear(screen.getByLabelText('Payment return URL'));
    await user.click(screen.getByRole('button', { name: 'Create store and wallet product' }));
    expect(window.confirm).toHaveBeenCalledWith('Create the product without a payment return URL?');
    expect(mockedPair).toHaveBeenCalledWith({ merchantId: '', privateKey: '', returnUrl: '' });
    expect(mockedCatalog).toHaveBeenCalledWith('', '');
    expect(await screen.findByText('Waffo Pancake store and product created.')).toBeTruthy();

    mockedCatalog.mockRejectedValueOnce(new Error('private_key=must-not-render'));
    rerender(<WaffoPancakeAdminPanel options={options} onSaved={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Verify and load catalog' }));
    expect(await screen.findByText('Unable to verify Waffo Pancake credentials.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('must-not-render');
  });
});
