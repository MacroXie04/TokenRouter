import { api } from '../../api';

const MAX_PRIVATE_KEY_BYTES = 16 * 1024;
const MAX_STORES = 500;
const MAX_PRODUCTS_PER_STORE = 1_000;
const MAX_TEXT = 256;
const MAX_TOP_UP_REFERENCE_AMOUNT = 4_294;
const MAX_UNIT_PRICE = 999_999.99;

export interface WaffoPancakeCatalogProduct {
  id: string;
  name: string;
  status: string;
}

export interface WaffoPancakeCatalogStore {
  id: string;
  name: string;
  status: string;
  prodEnabled: boolean;
  onetimeProducts: WaffoPancakeCatalogProduct[];
}

export interface WaffoPancakePairResult {
  storeId: string;
  storeName: string;
  productId: string;
  productName: string;
}

export interface WaffoPancakeSavedBinding {
  storeId: string;
  productId: string;
}

export interface WaffoPancakeProductOptions {
  storeId: string;
  products: WaffoPancakeCatalogProduct[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function validId(value: unknown, prefix: 'MER' | 'STO' | 'PROD'): value is string {
  if (typeof value !== 'string' || value.length !== prefix.length + 23 || !value.startsWith(`${prefix}_`)) {
    return false;
  }
  for (const character of value.slice(prefix.length + 1)) {
    if (!/[A-Za-z0-9]/.test(character)) return false;
  }
  return true;
}

function boundedText(value: unknown, allowEmpty = true): value is string {
  return typeof value === 'string' && value.length <= MAX_TEXT && (allowEmpty || value.length > 0);
}

function successfulData(value: unknown): Record<string, unknown> {
  if (!isRecord(value) || value.message !== 'success' || !isRecord(value.data)) {
    throw new Error('Waffo Pancake request failed');
  }
  return value.data;
}

function parseProduct(value: unknown): WaffoPancakeCatalogProduct {
  if (!isRecord(value) || !validId(value.id, 'PROD') ||
      !boundedText(value.name) || !boundedText(value.status)) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return { id: value.id, name: value.name, status: value.status };
}

function parseProducts(value: unknown): WaffoPancakeCatalogProduct[] {
  if (!Array.isArray(value) || value.length > MAX_PRODUCTS_PER_STORE) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return value.map(parseProduct);
}

export function parseWaffoPancakeCatalogResponse(value: unknown): WaffoPancakeCatalogStore[] {
  const data = successfulData(value);
  if (!Array.isArray(data.stores) || data.stores.length > MAX_STORES) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return data.stores.map((candidate) => {
    if (!isRecord(candidate) || !validId(candidate.id, 'STO') ||
        !boundedText(candidate.name) || !boundedText(candidate.status) ||
        typeof candidate.prodEnabled !== 'boolean') {
      throw new Error('Invalid Waffo Pancake response');
    }
    return {
      id: candidate.id,
      name: candidate.name,
      status: candidate.status,
      prodEnabled: candidate.prodEnabled,
      onetimeProducts: parseProducts(candidate.onetimeProducts),
    };
  });
}

export function parseWaffoPancakePairResponse(value: unknown): WaffoPancakePairResult {
  const data = successfulData(value);
  if (!validId(data.store_id, 'STO') || !validId(data.product_id, 'PROD') ||
      !boundedText(data.store_name) || !boundedText(data.product_name)) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return {
    storeId: data.store_id,
    storeName: data.store_name,
    productId: data.product_id,
    productName: data.product_name,
  };
}

export function parseWaffoPancakeSaveResponse(value: unknown): WaffoPancakeSavedBinding {
  const data = successfulData(value);
  if (!validId(data.store_id, 'STO') || !validId(data.product_id, 'PROD')) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return { storeId: data.store_id, productId: data.product_id };
}

export function parseWaffoPancakeProductOptionsResponse(value: unknown): WaffoPancakeProductOptions {
  const data = successfulData(value);
  if (!validId(data.store_id, 'STO')) throw new Error('Invalid Waffo Pancake response');
  return { storeId: data.store_id, products: parseProducts(data.products) };
}

function checkedCredentials(merchantId: string, privateKey: string) {
  const merchant = merchantId.trim();
  const key = privateKey.trim();
  if ((merchant === '') !== (key === '') ||
      (merchant !== '' && !validId(merchant, 'MER')) || key.length > MAX_PRIVATE_KEY_BYTES) {
    throw new Error('Invalid Waffo Pancake credentials');
  }
  return { merchant, key };
}

export async function listWaffoPancakeCatalog(
  merchantId: string,
  privateKey: string,
): Promise<WaffoPancakeCatalogStore[]> {
  const credentials = checkedCredentials(merchantId, privateKey);
  const response = await api.get<unknown>('/option/waffo-pancake/catalog', {
    params: credentials.merchant === ''
      ? {}
      : { merchant_id: credentials.merchant, private_key: credentials.key },
  });
  return parseWaffoPancakeCatalogResponse(response.data);
}

export async function createWaffoPancakePair(input: {
  merchantId: string;
  privateKey: string;
  returnUrl: string;
}): Promise<WaffoPancakePairResult> {
  const credentials = checkedCredentials(input.merchantId, input.privateKey);
  const returnUrl = input.returnUrl.trim();
  if (returnUrl.length > 2_048) throw new Error('Invalid Waffo Pancake return URL');
  const response = await api.post<unknown>('/option/waffo-pancake/pair', {
    merchant_id: credentials.merchant,
    private_key: credentials.key,
    return_url: returnUrl,
  });
  return parseWaffoPancakePairResponse(response.data);
}

export async function saveWaffoPancakeConfig(input: {
  merchantId: string;
  privateKey: string;
  returnUrl: string;
  storeId: string;
  productId: string;
  unitPrice: string;
  minTopUp: string;
}): Promise<WaffoPancakeSavedBinding> {
  const merchantId = input.merchantId.trim();
  const privateKey = input.privateKey.trim();
  const returnUrl = input.returnUrl.trim();
  const unitPrice = input.unitPrice.trim();
  const minTopUp = input.minTopUp.trim();
  if (!validId(merchantId, 'MER') || privateKey.length > MAX_PRIVATE_KEY_BYTES ||
      returnUrl.length > 2_048 || !validId(input.storeId, 'STO') ||
      !validId(input.productId, 'PROD') ||
      unitPrice.length > 64 || !/^(?:0|[1-9]\d*)(?:\.\d+)?$/.test(unitPrice) ||
      !Number.isFinite(Number(unitPrice)) || Number(unitPrice) <= 0 || Number(unitPrice) > MAX_UNIT_PRICE ||
      minTopUp.length > 32 || !/^[1-9]\d*$/.test(minTopUp) ||
      !Number.isSafeInteger(Number(minTopUp)) || Number(minTopUp) > MAX_TOP_UP_REFERENCE_AMOUNT) {
    throw new Error('Invalid Waffo Pancake configuration');
  }
  const response = await api.post<unknown>('/option/waffo-pancake/save', {
    merchant_id: merchantId,
    private_key: privateKey,
    return_url: returnUrl,
    store_id: input.storeId,
    product_id: input.productId,
    unit_price: unitPrice,
    min_top_up: minTopUp,
  });
  return parseWaffoPancakeSaveResponse(response.data);
}

export async function listWaffoPancakeSubscriptionProducts(): Promise<WaffoPancakeProductOptions> {
  const response = await api.get<unknown>('/option/waffo-pancake/subscription-product-options');
  return parseWaffoPancakeProductOptionsResponse(response.data);
}

export async function createWaffoPancakeSubscriptionProduct(input: {
  name: string;
  amount: string;
}): Promise<WaffoPancakeCatalogProduct> {
  const name = input.name.trim();
  const amount = input.amount.trim();
  if (name === '' || name.length > 128 ||
      amount.length > 32 || !/^(?:0|[1-9]\d{0,9})(?:\.\d{1,6})?$/.test(amount) ||
      !Number.isFinite(Number(amount)) || Number(amount) <= 0) {
    throw new Error('Invalid Waffo Pancake product');
  }
  const response = await api.post<unknown>('/option/waffo-pancake/subscription-product', { name, amount });
  const data = successfulData(response.data);
  if (!validId(data.product_id, 'PROD') || !boundedText(data.product_name, false)) {
    throw new Error('Invalid Waffo Pancake response');
  }
  return { id: data.product_id, name: data.product_name, status: '' };
}
