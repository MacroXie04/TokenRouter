export const PLAYGROUND_ENDPOINT = '/pg/chat/completions';
export const PLAYGROUND_GROUPS_ENDPOINT = '/api/user/self/groups';
export const PLAYGROUND_MODELS_ENDPOINT = '/api/user/models';
export const PLAYGROUND_SESSION_EXPIRED_EVENT = 'tokenrouter:session-expired';

export const PLAYGROUND_LIMITS = {
  groupCharacters: 128,
  modelCharacters: 512,
  systemCharacters: 32_768,
  messageCharacters: 65_536,
  conversationTurns: 32,
  requestBytes: 512 * 1024,
  catalogBytes: 512 * 1024,
  streamBytes: 4 * 1024 * 1024,
  outputBytes: 2 * 1024 * 1024,
} as const;

const MAX_GROUPS = 128;
const MAX_MODELS = 2_000;
const MAX_DESCRIPTION_CHARACTERS = 512;
const MAX_STREAM_EVENT_BYTES = 256 * 1024;
const MAX_STREAM_EVENTS = 20_000;

type UnknownRecord = Record<string, unknown>;

export type PlaygroundFetch = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

export interface PlaygroundGroup {
  id: string;
  description: string;
  ratio: number | null;
  automatic: boolean;
}

export interface PlaygroundParameters {
  temperature: number;
  topP: number;
  maxTokens: number;
  frequencyPenalty: number;
  presencePenalty: number;
  seed: number | null;
}

export interface PlaygroundConversationTurn {
  role: 'user' | 'assistant';
  content: string;
}

export interface PlaygroundChatMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
}

export interface PlaygroundChatRequest {
  model: string;
  group: string;
  messages: PlaygroundChatMessage[];
  stream: true;
  temperature: number;
  top_p: number;
  max_tokens: number;
  frequency_penalty: number;
  presence_penalty: number;
  seed?: number;
}

export interface PlaygroundStreamCallbacks {
  onContent: (chunk: string) => void;
  onReasoning: (chunk: string) => void;
}

export type PlaygroundErrorCode =
  | 'catalog'
  | 'http'
  | 'invalid-input'
  | 'invalid-response'
  | 'response-too-large'
  | 'truncated';

export class PlaygroundContractError extends Error {
  readonly code: PlaygroundErrorCode;
  readonly status?: number;

  constructor(code: PlaygroundErrorCode, status?: number) {
    super('The playground request could not be completed.');
    this.name = 'PlaygroundContractError';
    this.code = code;
    this.status = status;
  }
}

function record(value: unknown, code: PlaygroundErrorCode = 'invalid-response'): UnknownRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new PlaygroundContractError(code);
  }
  return value as UnknownRecord;
}

function encodedBytes(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function serializedBytes(value: unknown, code: PlaygroundErrorCode): number {
  try {
    return encodedBytes(JSON.stringify(value));
  } catch {
    throw new PlaygroundContractError(code);
  }
}

function hasDisallowedCharacter(value: string, allowWhitespaceControls: boolean): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return true;
      index += 1;
      continue;
    }
    if (code >= 0xdc00 && code <= 0xdfff) return true;
    if (code === 0x061c || code === 0x200e || code === 0x200f
        || (code >= 0x202a && code <= 0x202e)
        || (code >= 0x2066 && code <= 0x2069)) return true;
    if (code <= 0x1f && !(allowWhitespaceControls && (code === 0x09 || code === 0x0a || code === 0x0d))) {
      return true;
    }
    if (code >= 0x7f && code <= 0x9f) return true;
  }
  return false;
}

function identifier(value: unknown, maximum: number, code: PlaygroundErrorCode): string {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum
      || value.trim() !== value || hasDisallowedCharacter(value, false)) {
    throw new PlaygroundContractError(code);
  }
  return value;
}

function displayText(value: unknown, maximum: number, code: PlaygroundErrorCode): string {
  if (typeof value !== 'string' || value.length > maximum || hasDisallowedCharacter(value, false)) {
    throw new PlaygroundContractError(code);
  }
  return value;
}

function chatText(value: unknown, maximum: number): string {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum
      || value.trim().length === 0 || hasDisallowedCharacter(value, true)) {
    throw new PlaygroundContractError('invalid-input');
  }
  return value;
}

function finiteNumber(value: unknown, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new PlaygroundContractError('invalid-input');
  }
  return value;
}

function safeInteger(value: unknown, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new PlaygroundContractError('invalid-input');
  }
  return value as number;
}

function successfulEnvelope(value: unknown): UnknownRecord {
  if (serializedBytes(value, 'catalog') > PLAYGROUND_LIMITS.catalogBytes) {
    throw new PlaygroundContractError('response-too-large');
  }
  const envelope = record(value, 'catalog');
  if (envelope.success !== true || !Object.prototype.hasOwnProperty.call(envelope, 'data')) {
    throw new PlaygroundContractError('catalog');
  }
  return envelope;
}

function groupOrder(left: PlaygroundGroup, right: PlaygroundGroup): number {
  const priority = (group: PlaygroundGroup) => group.id === 'default' ? 0 : group.id === 'auto' ? 1 : 2;
  return priority(left) - priority(right) || left.id.localeCompare(right.id, 'en');
}

export function parsePlaygroundGroups(value: unknown): PlaygroundGroup[] {
  const envelope = successfulEnvelope(value);
  const data = record(envelope.data, 'catalog');
  const entries = Object.entries(data);
  if (entries.length > MAX_GROUPS) throw new PlaygroundContractError('catalog');
  const groups = entries.map(([rawID, rawInfo]) => {
    const id = identifier(rawID, PLAYGROUND_LIMITS.groupCharacters, 'catalog');
    const info = record(rawInfo, 'catalog');
    const description = displayText(info.desc ?? '', MAX_DESCRIPTION_CHARACTERS, 'catalog');
    const automatic = id === 'auto';
    let ratio: number | null;
    if (automatic && (info.ratio === '自动' || info.ratio === 'auto')) {
      ratio = null;
    } else if (typeof info.ratio === 'number' && Number.isFinite(info.ratio)
        && info.ratio >= 0 && info.ratio <= 1_000_000) {
      ratio = info.ratio;
    } else {
      throw new PlaygroundContractError('catalog');
    }
    return { id, description, ratio, automatic };
  });
  return groups.sort(groupOrder);
}

export function parsePlaygroundModels(value: unknown): string[] {
  const envelope = successfulEnvelope(value);
  if (!Array.isArray(envelope.data) || envelope.data.length > MAX_MODELS) {
    throw new PlaygroundContractError('catalog');
  }
  const models = envelope.data.map((item) => identifier(
    item,
    PLAYGROUND_LIMITS.modelCharacters,
    'catalog',
  ));
  if (new Set(models).size !== models.length) throw new PlaygroundContractError('catalog');
  return models;
}

function notifySessionExpired(status: number): void {
  if (status === 401 && typeof window !== 'undefined') {
    window.dispatchEvent(new Event(PLAYGROUND_SESSION_EXPIRED_EVENT));
  }
}

function checkedContentLength(response: Response, maximum: number): void {
  const raw = response.headers.get('content-length');
  if (raw === null) return;
  if (!/^\d{1,16}$/u.test(raw) || Number(raw) > maximum) {
    throw new PlaygroundContractError('response-too-large');
  }
}

async function readLimitedText(response: Response, maximum: number): Promise<string> {
  try {
    checkedContentLength(response, maximum);
  } catch (error) {
    void response.body?.cancel();
    throw error;
  }
  if (!response.body) throw new PlaygroundContractError('invalid-response');
  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', { fatal: true });
  let total = 0;
  let output = '';
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maximum) throw new PlaygroundContractError('response-too-large');
      output += decoder.decode(value, { stream: true });
    }
    output += decoder.decode();
    return output;
  } catch (error) {
    void reader.cancel();
    if (error instanceof PlaygroundContractError) throw error;
    throw new PlaygroundContractError('invalid-response');
  }
}

async function loadCatalog(
  url: string,
  signal: AbortSignal | undefined,
  fetcher: PlaygroundFetch,
): Promise<unknown> {
  const response = await fetcher(url, {
    method: 'GET',
    credentials: 'same-origin',
    cache: 'no-store',
    headers: { Accept: 'application/json' },
    signal,
  });
  notifySessionExpired(response.status);
  if (!response.ok) {
    void response.body?.cancel();
    throw new PlaygroundContractError('http', response.status);
  }
  const text = await readLimitedText(response, PLAYGROUND_LIMITS.catalogBytes);
  try {
    return JSON.parse(text) as unknown;
  } catch {
    throw new PlaygroundContractError('catalog');
  }
}

export async function loadPlaygroundGroups(
  signal?: AbortSignal,
  fetcher: PlaygroundFetch = fetch,
): Promise<PlaygroundGroup[]> {
  return parsePlaygroundGroups(await loadCatalog(PLAYGROUND_GROUPS_ENDPOINT, signal, fetcher));
}

export async function loadPlaygroundModels(
  group: string,
  signal?: AbortSignal,
  fetcher: PlaygroundFetch = fetch,
): Promise<string[]> {
  const validatedGroup = identifier(group, PLAYGROUND_LIMITS.groupCharacters, 'invalid-input');
  const query = new URLSearchParams({ group: validatedGroup });
  return parsePlaygroundModels(await loadCatalog(
    `${PLAYGROUND_MODELS_ENDPOINT}?${query.toString()}`,
    signal,
    fetcher,
  ));
}

export function buildPlaygroundRequest({
  model,
  group,
  systemPrompt,
  conversation,
  message,
  parameters,
}: {
  model: string;
  group: string;
  systemPrompt: string;
  conversation: PlaygroundConversationTurn[];
  message: string;
  parameters: PlaygroundParameters;
}): PlaygroundChatRequest {
  const safeModel = identifier(model, PLAYGROUND_LIMITS.modelCharacters, 'invalid-input');
  const safeGroup = identifier(group, PLAYGROUND_LIMITS.groupCharacters, 'invalid-input');
  if (!Array.isArray(conversation) || conversation.length > PLAYGROUND_LIMITS.conversationTurns) {
    throw new PlaygroundContractError('invalid-input');
  }
  const messages: PlaygroundChatMessage[] = [];
  if (systemPrompt !== '') {
    messages.push({ role: 'system', content: chatText(systemPrompt, PLAYGROUND_LIMITS.systemCharacters) });
  }
  for (const turn of conversation) {
    if (!turn || (turn.role !== 'user' && turn.role !== 'assistant')) {
      throw new PlaygroundContractError('invalid-input');
    }
    messages.push({ role: turn.role, content: chatText(turn.content, PLAYGROUND_LIMITS.messageCharacters) });
  }
  messages.push({ role: 'user', content: chatText(message, PLAYGROUND_LIMITS.messageCharacters) });

  const payload: PlaygroundChatRequest = {
    model: safeModel,
    group: safeGroup,
    messages,
    stream: true,
    temperature: finiteNumber(parameters.temperature, 0, 2),
    top_p: finiteNumber(parameters.topP, 0, 1),
    max_tokens: safeInteger(parameters.maxTokens, 1, 32_768),
    frequency_penalty: finiteNumber(parameters.frequencyPenalty, -2, 2),
    presence_penalty: finiteNumber(parameters.presencePenalty, -2, 2),
  };
  if (parameters.seed !== null) {
    payload.seed = safeInteger(parameters.seed, -2_147_483_648, 2_147_483_647);
  }
  if (serializedBytes(payload, 'invalid-input') > PLAYGROUND_LIMITS.requestBytes) {
    throw new PlaygroundContractError('invalid-input');
  }
  return payload;
}

function streamRecord(value: unknown): UnknownRecord {
  return record(value, 'invalid-response');
}

function streamChunkText(value: unknown): string | null {
  if (value === undefined || value === null || value === '') return null;
  if (typeof value !== 'string' || hasDisallowedCharacter(value, true)) {
    throw new PlaygroundContractError('invalid-response');
  }
  return value;
}

function parseStreamData(
  data: string,
  output: { bytes: number; content: string[]; reasoning: string[] },
): boolean {
  if (data === '[DONE]') return true;
  if (encodedBytes(data) > MAX_STREAM_EVENT_BYTES) {
    throw new PlaygroundContractError('response-too-large');
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(data) as unknown;
  } catch {
    throw new PlaygroundContractError('invalid-response');
  }
  const chunk = streamRecord(parsed);
  if (Object.prototype.hasOwnProperty.call(chunk, 'error')) {
    throw new PlaygroundContractError('http');
  }
  if (!Array.isArray(chunk.choices) || chunk.choices.length > 16) {
    throw new PlaygroundContractError('invalid-response');
  }
  for (const rawChoice of chunk.choices) {
    const choice = streamRecord(rawChoice);
    const delta = streamRecord(choice.delta);
    const updates: Array<['reasoning' | 'content', string | null]> = [
      ['reasoning', streamChunkText(delta.reasoning_content)],
      ['content', streamChunkText(delta.content)],
    ];
    for (const [kind, value] of updates) {
      if (value === null) continue;
      output.bytes += encodedBytes(value);
      if (output.bytes > PLAYGROUND_LIMITS.outputBytes) {
        throw new PlaygroundContractError('response-too-large');
      }
      output[kind].push(value);
    }
  }
  return false;
}

export async function streamPlaygroundCompletion(
  payload: PlaygroundChatRequest,
  callbacks: PlaygroundStreamCallbacks,
  signal?: AbortSignal,
  fetcher: PlaygroundFetch = fetch,
): Promise<void> {
  const body = JSON.stringify(payload);
  if (encodedBytes(body) > PLAYGROUND_LIMITS.requestBytes) {
    throw new PlaygroundContractError('invalid-input');
  }
  const response = await fetcher(PLAYGROUND_ENDPOINT, {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    headers: {
      Accept: 'text/event-stream',
      'Content-Type': 'application/json',
    },
    body,
    signal,
  });
  notifySessionExpired(response.status);
  if (!response.ok) {
    void response.body?.cancel();
    throw new PlaygroundContractError('http', response.status);
  }
  try {
    checkedContentLength(response, PLAYGROUND_LIMITS.streamBytes);
  } catch (error) {
    void response.body?.cancel();
    throw error;
  }
  const contentType = response.headers.get('content-type')?.toLowerCase() ?? '';
  if (!contentType.startsWith('text/event-stream')) {
    void response.body?.cancel();
    throw new PlaygroundContractError('invalid-response');
  }
  if (!response.body) throw new PlaygroundContractError('invalid-response');

  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', { fatal: true });
  const output = { bytes: 0, content: [] as string[], reasoning: [] as string[] };
  let receivedBytes = 0;
  let buffer = '';
  let eventData: string[] = [];
  let eventDataBytes = 0;
  let eventCount = 0;
  let doneEvent = false;
  let firstLine = true;

  const flushEvent = () => {
    if (eventData.length === 0) return;
    eventCount += 1;
    if (eventCount > MAX_STREAM_EVENTS) {
      throw new PlaygroundContractError('response-too-large');
    }
    const data = eventData.join('\n');
    eventData = [];
    eventDataBytes = 0;
    if (parseStreamData(data, output)) doneEvent = true;
  };
  const flushUpdates = () => {
    if (output.reasoning.length > 0) {
      callbacks.onReasoning(output.reasoning.join(''));
      output.reasoning = [];
    }
    if (output.content.length > 0) {
      callbacks.onContent(output.content.join(''));
      output.content = [];
    }
  };
  const consumeLine = (rawLine: string) => {
    let line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
    if (firstLine) {
      firstLine = false;
      if (line.startsWith('\ufeff')) line = line.slice(1);
    }
    if (line === '') {
      flushEvent();
      return;
    }
    if (line.startsWith(':')) return;
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'data') {
      eventDataBytes += encodedBytes(value) + (eventData.length === 0 ? 0 : 1);
      if (eventDataBytes > MAX_STREAM_EVENT_BYTES) {
        throw new PlaygroundContractError('response-too-large');
      }
      eventData.push(value);
    }
  };

  try {
    while (!doneEvent) {
      const read = await reader.read();
      if (read.done) break;
      receivedBytes += read.value.byteLength;
      if (receivedBytes > PLAYGROUND_LIMITS.streamBytes) {
        throw new PlaygroundContractError('response-too-large');
      }
      buffer += decoder.decode(read.value, { stream: true });
      let newline = buffer.indexOf('\n');
      while (newline >= 0 && !doneEvent) {
        consumeLine(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        newline = buffer.indexOf('\n');
      }
      flushUpdates();
      if (encodedBytes(buffer) > MAX_STREAM_EVENT_BYTES) {
        throw new PlaygroundContractError('response-too-large');
      }
    }
    if (doneEvent) {
      flushUpdates();
      void reader.cancel();
      return;
    }
    buffer += decoder.decode();
    if (buffer !== '') consumeLine(buffer);
    flushEvent();
    flushUpdates();
    if (!doneEvent) throw new PlaygroundContractError('truncated');
  } catch (error) {
    void reader.cancel();
    if (signal?.aborted) throw error;
    if (error instanceof PlaygroundContractError) throw error;
    throw new PlaygroundContractError('invalid-response');
  }
}
