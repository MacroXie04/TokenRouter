import { api } from '../../shared/api/client';

const MAX_RESPONSE_BYTES = 512 * 1024;
const MAX_PRESETS = 64;
const MAX_NAME = 128;
const MAX_URL = 4096;
const MAX_KEYS = 50;
const responseLimits = { maxContentLength: MAX_RESPONSE_BYTES, maxBodyLength: MAX_RESPONSE_BYTES };

type UnknownRecord = Record<string, unknown>;

export type ChatLinkType = 'web' | 'custom-protocol' | 'shortcut';

export interface ChatPreset {
  id: string;
  name: string;
  url: string;
  type: ChatLinkType;
  requiresKey: boolean;
}

export interface ChatContext {
  serverAddress: string;
  presets: ChatPreset[];
}

export class ChatContractError extends Error {
  constructor() {
    super('Invalid chat launcher response');
    this.name = 'ChatContractError';
  }
}

function record(value: unknown): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new ChatContractError();
  return value as UnknownRecord;
}

function bounded(value: unknown): void {
  let serialized: string;
  try {
    serialized = JSON.stringify(value);
  } catch {
    throw new ChatContractError();
  }
  if (serialized.length > MAX_RESPONSE_BYTES) throw new ChatContractError();
}

function text(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length > maximum || [...value].some((character) => {
    const code = character.charCodeAt(0);
    return code <= 31 || code === 127;
  })) {
    throw new ChatContractError();
  }
  return value;
}

function integer(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new ChatContractError();
  }
  return value as number;
}

function safeAbsoluteURL(raw: string): URL | null {
  try {
    const parsed = new URL(raw);
    if (parsed.username || parsed.password) return null;
    const protocol = parsed.protocol.toLowerCase();
    if (protocol === 'https:') return parsed;
    if (protocol !== 'http:') return null;
    const host = parsed.hostname.toLowerCase().replace(/\.$/, '');
    if (host === 'localhost' || host === '127.0.0.1' || host === '[::1]') return parsed;
  } catch {
    return null;
  }
  return null;
}

function normalizedServerAddress(value: unknown, fallbackOrigin: string): string {
  const raw = value === undefined || value === null ? '' : text(value, 2048).trim();
  const configured = raw ? safeAbsoluteURL(raw) : null;
  const fallback = safeAbsoluteURL(fallbackOrigin);
  if (configured) return configured.origin + configured.pathname.replace(/\/$/, '');
  if (fallback) return fallback.origin + fallback.pathname.replace(/\/$/, '');
  throw new ChatContractError();
}

function classifyPresetURL(raw: string): ChatLinkType {
  const probe = raw
    .replaceAll('{key}', 'sk-placeholder')
    .replaceAll('{address}', 'https%3A%2F%2Frouter.example.invalid')
    .replaceAll('{cherryConfig}', 'config')
    .replaceAll('{aionuiConfig}', 'config')
    .replaceAll('{deepchatConfig}', 'config');
  if (safeAbsoluteURL(probe)) return 'web';
  const scheme = /^([A-Za-z][A-Za-z0-9+.-]{0,31}):/.exec(probe)?.[1]?.toLowerCase();
  if (scheme && !['javascript', 'data', 'file', 'vbscript', 'http', 'https'].includes(scheme)) {
    return 'custom-protocol';
  }
  if (/^[A-Za-z][A-Za-z0-9+.-]{0,31}$/.test(probe)) return 'shortcut';
  throw new ChatContractError();
}

function parsePreset(value: unknown, index: number, seen: Set<string>): ChatPreset {
  const raw = record(value);
  const entries = Object.entries(raw);
  if (entries.length !== 1) throw new ChatContractError();
  const name = text(entries[0][0], MAX_NAME);
  const url = text(entries[0][1], MAX_URL);
  if (!name || name !== name.trim() || !url || url !== url.trim() || seen.has(name)) throw new ChatContractError();
  seen.add(name);
  return {
    id: String(index),
    name,
    url,
    type: classifyPresetURL(url),
    requiresKey: ['{key}', '{cherryConfig}', '{aionuiConfig}', '{deepchatConfig}'].some((token) => url.includes(token)),
  };
}

export function parseChatStatusResponse(value: unknown, fallbackOrigin: string): ChatContext {
  bounded(value);
  const envelope = record(value);
  if (envelope.success !== true) throw new ChatContractError();
  const data = record(envelope.data);
  if (!Array.isArray(data.chats) || data.chats.length > MAX_PRESETS) throw new ChatContractError();
  const seen = new Set<string>();
  return {
    serverAddress: normalizedServerAddress(data.server_address, fallbackOrigin),
    presets: data.chats.map((entry, index) => parsePreset(entry, index, seen)),
  };
}

function normalizeKey(value: unknown): string {
  const raw = text(value, 128).trim();
  const key = raw.startsWith('sk-') ? raw : `sk-${raw}`;
  if (!/^sk-[A-Za-z0-9_-]{8,125}$/.test(key)) throw new ChatContractError();
  return key;
}

function encodedClientConfig(serverAddress: string, apiKey: string): string {
  const serialized = JSON.stringify({ id: 'tokenrouter', baseUrl: serverAddress, apiKey });
  return encodeURIComponent(btoa(serialized));
}

export function resolveChatURL(preset: ChatPreset, rawKey: string | undefined, serverAddress: string): string | null {
  const baseAddress = normalizedServerAddress(serverAddress, serverAddress);
  const apiKey = preset.requiresKey ? normalizeKey(rawKey) : '';
  let resolved = preset.url;
  const config = preset.requiresKey ? encodedClientConfig(baseAddress, apiKey) : '';
  resolved = resolved
    .replaceAll('{cherryConfig}', config)
    .replaceAll('{aionuiConfig}', config)
    .replaceAll('{deepchatConfig}', config)
    .replaceAll('{address}', encodeURIComponent(baseAddress))
    .replaceAll('{key}', apiKey);

  if (preset.type === 'web') return safeAbsoluteURL(resolved)?.toString() ?? null;
  if (preset.type === 'shortcut') return null;
  const originalScheme = /^([A-Za-z][A-Za-z0-9+.-]{0,31}):/.exec(preset.url)?.[1]?.toLowerCase();
  const resolvedScheme = /^([A-Za-z][A-Za-z0-9+.-]{0,31}):/.exec(resolved)?.[1]?.toLowerCase();
  if (!originalScheme || originalScheme !== resolvedScheme || ['javascript', 'data', 'file', 'vbscript'].includes(resolvedScheme ?? '')) {
    return null;
  }
  return resolved;
}

export async function loadChatContext(signal?: AbortSignal, fallbackOrigin = window.location.origin): Promise<ChatContext> {
  const response = await api.get<unknown>('/status', { signal, ...responseLimits });
  return parseChatStatusResponse(response.data, fallbackOrigin);
}

export async function loadActiveChatKey(signal?: AbortSignal): Promise<string> {
  const listResponse = await api.get<unknown>('/token/', {
    params: { p: 1, page_size: MAX_KEYS },
    signal,
    ...responseLimits,
  });
  bounded(listResponse.data);
  const envelope = record(listResponse.data);
  if (envelope.success !== true) throw new ChatContractError();
  const page = record(envelope.data);
  const pageSize = integer(page.page_size, 1, MAX_KEYS);
  if (!Array.isArray(page.items) || page.items.length > pageSize) throw new ChatContractError();
  integer(page.page, 1, 1_000_000);
  integer(page.total, 0, 10_000_000);
  let activeID: number | null = null;
  for (const item of page.items) {
    const token = record(item);
    const id = integer(token.id, 1, 2_147_483_647);
    const status = integer(token.status, 1, 4);
    if (status === 1 && activeID === null) activeID = id;
  }
  if (activeID === null) throw new ChatContractError();

  const keyResponse = await api.post<unknown>(`/token/${activeID}/key`, undefined, { signal, ...responseLimits });
  bounded(keyResponse.data);
  const keyEnvelope = record(keyResponse.data);
  if (keyEnvelope.success !== true) throw new ChatContractError();
  return normalizeKey(record(keyEnvelope.data).key);
}
