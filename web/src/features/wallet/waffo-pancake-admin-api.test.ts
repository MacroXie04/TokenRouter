import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import {
  createWaffoPancakePair,
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeCatalog,
  listWaffoPancakeSubscriptionProducts,
  parseWaffoPancakeCatalogResponse,
  parseWaffoPancakePairResponse,
  saveWaffoPancakeConfig,
} from './waffo-pancake-admin-api';

vi.mock('../../api', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);
const merchantId = 'MER_AbCdEfGhIjKlMnOpQrStUv';
const storeId = 'STO_AbCdEfGhIjKlMnOpQrStUv';
const productId = 'PROD_AbCdEfGhIjKlMnOpQrStUv';
const planProductId = 'PROD_ZyXwVuTsRqPoNmLkJiHgFe';

const catalogBody = {
  message: 'success',
  data: {
    stores: [{
      id: storeId,
      name: 'Primary store',
      status: 'active',
      prodEnabled: true,
      onetimeProducts: [{ id: productId, name: 'Wallet', status: 'active' }],
    }],
  },
};

beforeEach(() => {
  vi.resetAllMocks();
});

describe('Waffo Pancake administrator API', () => {
  it('validates bounded response envelopes and sends the exact compatibility requests', async () => {
    mockedGet
      .mockResolvedValueOnce({ data: catalogBody })
      .mockResolvedValueOnce({ data: catalogBody })
      .mockResolvedValueOnce({ data: {
        message: 'success', data: { store_id: storeId, products: [{ id: planProductId, name: 'Monthly', status: 'active' }] },
      } });
    mockedPost
      .mockResolvedValueOnce({ data: {
        message: 'success', data: { store_id: storeId, store_name: 'Primary store', product_id: productId, product_name: 'Wallet' },
      } })
      .mockResolvedValueOnce({ data: { message: 'success', data: { store_id: storeId, product_id: productId } } })
      .mockResolvedValueOnce({ data: {
        message: 'success', data: { store_id: storeId, product_id: planProductId, product_name: 'Monthly' },
      } });

    await expect(listWaffoPancakeCatalog('', '')).resolves.toEqual(catalogBody.data.stores);
    await expect(listWaffoPancakeCatalog(merchantId, 'private-key')).resolves.toEqual(catalogBody.data.stores);
    await expect(createWaffoPancakePair({ merchantId: '', privateKey: '', returnUrl: 'https://example.test/wallet/' })).resolves.toMatchObject({
      storeId, productId,
    });
    await expect(saveWaffoPancakeConfig({
      merchantId,
      privateKey: '',
      returnUrl: 'https://example.test/wallet',
      storeId,
      productId,
      unitPrice: '2.5',
      minTopUp: '30',
    })).resolves.toEqual({ storeId, productId });
    await expect(listWaffoPancakeSubscriptionProducts()).resolves.toMatchObject({ storeId });
    await expect(createWaffoPancakeSubscriptionProduct({ name: ' Monthly ', amount: '19.95' })).resolves.toEqual({
      id: planProductId, name: 'Monthly', status: '',
    });

    expect(mockedGet).toHaveBeenNthCalledWith(1, '/option/waffo-pancake/catalog', { params: {} });
    expect(mockedGet).toHaveBeenNthCalledWith(2, '/option/waffo-pancake/catalog', {
      params: { merchant_id: merchantId, private_key: 'private-key' },
    });
    expect(mockedGet).toHaveBeenNthCalledWith(3, '/option/waffo-pancake/subscription-product-options');
    expect(mockedPost).toHaveBeenNthCalledWith(1, '/option/waffo-pancake/pair', {
      merchant_id: '', private_key: '', return_url: 'https://example.test/wallet/',
    });
    expect(mockedPost).toHaveBeenNthCalledWith(2, '/option/waffo-pancake/save', {
      merchant_id: merchantId,
      private_key: '',
      return_url: 'https://example.test/wallet',
      store_id: storeId,
      product_id: productId,
      unit_price: '2.5',
      min_top_up: '30',
    });
    expect(mockedPost).toHaveBeenNthCalledWith(3, '/option/waffo-pancake/subscription-product', {
      name: 'Monthly', amount: '19.95',
    });
  });

  it('fails closed on malformed or oversized provider-controlled catalog data', () => {
    expect(() => parseWaffoPancakeCatalogResponse({
      message: 'success', data: { stores: [{ ...catalogBody.data.stores[0], id: 'secret-store-id' }] },
    })).toThrow('Invalid Waffo Pancake response');
    expect(() => parseWaffoPancakeCatalogResponse({
      message: 'success', data: { stores: new Array(501).fill(catalogBody.data.stores[0]) },
    })).toThrow('Invalid Waffo Pancake response');
    expect(() => parseWaffoPancakePairResponse({ message: 'error', data: 'private key sk-must-not-escape' }))
      .toThrow('Waffo Pancake request failed');
  });

  it('rejects mixed credentials and unsafe numeric inputs before any request', async () => {
    await expect(listWaffoPancakeCatalog(merchantId, '')).rejects.toThrow('Invalid Waffo Pancake credentials');
    await expect(saveWaffoPancakeConfig({
      merchantId,
      privateKey: '',
      returnUrl: '',
      storeId,
      productId,
      unitPrice: 'Infinity',
      minTopUp: '30',
    })).rejects.toThrow('Invalid Waffo Pancake configuration');
    for (const [unitPrice, minTopUp] of [
      ['1000000', '30'],
      ['2.5', '4295'],
    ]) {
      await expect(saveWaffoPancakeConfig({
        merchantId,
        privateKey: '',
        returnUrl: '',
        storeId,
        productId,
        unitPrice,
        minTopUp,
      })).rejects.toThrow('Invalid Waffo Pancake configuration');
    }
    await expect(createWaffoPancakeSubscriptionProduct({ name: 'Plan', amount: '-1' }))
      .rejects.toThrow('Invalid Waffo Pancake product');
    expect(mockedGet).not.toHaveBeenCalled();
    expect(mockedPost).not.toHaveBeenCalled();
  });
});
