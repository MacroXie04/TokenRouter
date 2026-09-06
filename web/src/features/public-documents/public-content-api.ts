import { api } from '../../shared/api/client';
export const MAX_PUBLIC_CONTENT_CHARACTERS = 1_000_000;
const PUBLIC_CONTENT_RESPONSE_BYTES = 6 * 1024 * 1024;
type UnknownRecord = Record<string, unknown>;
export type PublicContentPath = '/notice' | '/home_page_content' | '/about' | '/user-agreement' | '/privacy-policy';
export class PublicHomeContractError extends Error {
  constructor() {
    super('Invalid public home API response');
    this.name = 'PublicHomeContractError';
  }
}

function fail(): never {
  throw new PublicHomeContractError();
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as UnknownRecord;
}

function boundedPayload(value: unknown, maximum: number): void {
  let encoded: string | undefined;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail();
  }
  if (encoded === undefined || new TextEncoder().encode(encoded).byteLength > maximum) fail();
}

function envelopeData(value: unknown, maximum: number): unknown {
  boundedPayload(value, maximum);
  const envelope = record(value);
  if (envelope.success !== true) fail();
  return envelope.data;
}

export function parsePublicContentResponse(value: unknown): string {
  const data = envelopeData(value, PUBLIC_CONTENT_RESPONSE_BYTES);
  if (typeof data !== 'string' || data.length > MAX_PUBLIC_CONTENT_CHARACTERS) fail();
  return data.trim();
}

export async function loadPublicContent(path: PublicContentPath, signal?: AbortSignal): Promise<string> {
  const response = await api.get<unknown>(path, {
    signal,
    maxContentLength: PUBLIC_CONTENT_RESPONSE_BYTES,
    maxBodyLength: PUBLIC_CONTENT_RESPONSE_BYTES,
  });
  return parsePublicContentResponse(response.data);
}
