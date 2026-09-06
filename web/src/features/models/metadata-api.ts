import { api } from '../../shared/api/client';
import { parseNullMutation } from './deployment-contracts';
import { MetadataPage, ModelMetadata, ModelMutationInput, ModelQuery, SyncLocale, SyncPreview, SyncResult, VendorMetadata, VendorMutationInput, VendorPage, parseMissingModelsResponse, parseModelPageResponse, parseModelResponse, parseSyncPreviewResponse, parseSyncResultResponse, parseVendorPageResponse, parseVendorResponse } from './metadata-contracts';
import { MAX_ID, MAX_METADATA_BYTES, MAX_PAGE, MAX_RELATED_ITEMS, MAX_ROWS, ModelsContractError, SYNC_FIELDS, UnknownRecord, binary, boundedArray, integer, responseData, responseLimits, trimInput, trimInputRunes, validID } from './model-response';

export async function loadModels(query: ModelQuery, signal?: AbortSignal): Promise<MetadataPage> {
  const params: Record<string, string | number> = {
    p: integer(query.page, 1, MAX_PAGE),
    page_size: integer(query.pageSize, 1, MAX_ROWS),
  };
  const keyword = trimInput(query.keyword, 256);
  const vendor = trimInput(query.vendor, 128);
  if (keyword) params.keyword = keyword;
  if (vendor) params.vendor = vendor;
  if (query.status) params.status = query.status;
  if (query.syncOfficial) params.sync_official = query.syncOfficial;
  const path = keyword ? '/models/search' : '/models/';
  return parseModelPageResponse(await responseData(api.get<unknown>(path, { params, signal, ...responseLimits })));
}

export async function loadVendors(
  signal?: AbortSignal,
  keyword = '',
  page = 1,
  pageSize = 100,
): Promise<VendorPage> {
  const safeKeyword = trimInput(keyword, 256);
  const path = safeKeyword ? '/vendors/search' : '/vendors/';
  const params: Record<string, string | number> = {
    p: integer(page, 1, MAX_PAGE),
    page_size: integer(pageSize, 1, MAX_ROWS),
  };
  if (safeKeyword) params.keyword = safeKeyword;
  return parseVendorPageResponse(await responseData(api.get<unknown>(path, { params, signal, ...responseLimits })));
}

export async function getModel(id: number, signal?: AbortSignal): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.get<unknown>(`/models/${validID(id)}`, { signal, ...responseLimits })));
}

export async function getVendor(id: number, signal?: AbortSignal): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.get<unknown>(`/vendors/${validID(id)}`, { signal, ...responseLimits })));
}

export function modelBody(input: ModelMutationInput, includeID: boolean): UnknownRecord {
  const modelName = trimInputRunes(input.modelName, 128, 512, true);
  const endpoints = trimInput(input.endpoints, MAX_METADATA_BYTES);
  if (endpoints) {
    try {
      JSON.parse(endpoints);
    } catch {
      throw new ModelsContractError();
    }
  }
  const body: UnknownRecord = {
    model_name: modelName,
    description: trimInput(input.description, MAX_METADATA_BYTES),
    icon: trimInputRunes(input.icon, 128, 512),
    tags: trimInputRunes(input.tags, 255, 1_020),
    vendor_id: integer(input.vendorId, 0, MAX_ID),
    endpoints,
    status: binary(input.status),
    sync_official: binary(input.syncOfficial),
    name_rule: integer(input.nameRule, 0, 3),
  };
  if (includeID) body.id = validID(input.id as number);
  return body;
}

export async function createModel(input: ModelMutationInput): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.post<unknown>('/models/', modelBody(input, false), responseLimits)));
}

export async function updateModel(input: ModelMutationInput & { id: number }): Promise<ModelMetadata> {
  return parseModelResponse(await responseData(api.put<unknown>('/models/', modelBody(input, true), responseLimits)));
}

export async function setModelStatus(id: number, status: 0 | 1): Promise<void> {
  const value = await responseData(api.put<unknown>('/models/?status_only=true', {
    id: validID(id), status: binary(status),
  }, responseLimits));
  // The backend returns the updated row; parsing it prevents accidental secret expansion.
  parseModelResponse(value);
}

export async function deleteModel(id: number): Promise<void> {
  parseNullMutation(await responseData(api.delete<unknown>(`/models/${validID(id)}`, responseLimits)));
}

export function vendorBody(input: VendorMutationInput, includeID: boolean): UnknownRecord {
  const body: UnknownRecord = {
    name: trimInputRunes(input.name, 128, 512, true),
    description: trimInput(input.description, MAX_METADATA_BYTES),
    icon: trimInputRunes(input.icon, 128, 512),
    status: binary(input.status),
  };
  if (includeID) body.id = validID(input.id as number);
  return body;
}

export async function createVendor(input: VendorMutationInput): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.post<unknown>('/vendors/', vendorBody(input, false), responseLimits)));
}

export async function updateVendor(input: VendorMutationInput & { id: number }): Promise<VendorMetadata> {
  return parseVendorResponse(await responseData(api.put<unknown>('/vendors/', vendorBody(input, true), responseLimits)));
}

export async function deleteVendor(id: number): Promise<void> {
  parseNullMutation(await responseData(api.delete<unknown>(`/vendors/${validID(id)}`, responseLimits)));
}

export async function loadMissingModels(signal?: AbortSignal): Promise<string[]> {
  return parseMissingModelsResponse(await responseData(api.get<unknown>('/models/missing', { signal, ...responseLimits })));
}

export function localeParam(locale: SyncLocale): Record<string, string> | undefined {
  if (!['', 'en', 'ja', 'zh-cn', 'zh-tw'].includes(locale)) throw new ModelsContractError();
  return locale ? { locale } : undefined;
}

export async function previewUpstream(locale: SyncLocale, signal?: AbortSignal): Promise<SyncPreview> {
  return parseSyncPreviewResponse(await responseData(api.get<unknown>('/models/sync_upstream/preview', {
    params: localeParam(locale), signal, ...responseLimits,
  })));
}

export async function syncUpstream(locale: SyncLocale, preview: SyncPreview): Promise<SyncResult> {
  const missing = boundedArray(preview.missing, MAX_RELATED_ITEMS).map((name) => trimInput(name as string, 512, true));
  if (new Set(missing).size !== missing.length || preview.conflicts.length > 1_000) throw new ModelsContractError();
  const seenModels = new Set<string>();
  const overwrite = preview.conflicts.map((conflict) => ({
    model_name: (() => {
      const name = trimInput(conflict.modelName, 512, true);
      if (seenModels.has(name)) throw new ModelsContractError();
      seenModels.add(name);
      return name;
    })(),
    fields: (() => {
      if (conflict.fields.length < 1 || conflict.fields.length > SYNC_FIELDS.size
        || new Set(conflict.fields).size !== conflict.fields.length) throw new ModelsContractError();
      return conflict.fields.map((field) => {
        if (!SYNC_FIELDS.has(field)) throw new ModelsContractError();
        return field;
      });
    })(),
  }));
  return parseSyncResultResponse(await responseData(api.post<unknown>('/models/sync_upstream', {
    locale: locale || '', overwrite,
  }, responseLimits)));
}
