import { api } from '../../api';
import { parsePricingCatalogResponse, type PricingCatalog } from './catalog';

const CATALOG_RESPONSE_BYTES = 8 * 1024 * 1024;

export async function loadPricingCatalog(signal?: AbortSignal): Promise<PricingCatalog> {
  const response = await api.get<unknown>('/pricing', {
    signal,
    maxContentLength: CATALOG_RESPONSE_BYTES,
    maxBodyLength: CATALOG_RESPONSE_BYTES,
  });
  return parsePricingCatalogResponse(response.data);
}
