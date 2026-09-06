import type { AxiosRequestConfig, GenericAbortSignal, InternalAxiosRequestConfig } from 'axios';
import axios from 'axios';
import type { ApiResponse } from './contracts';
export type { ApiResponse, Token, User, UserPermissions } from './contracts';

// Shared axios instance: same-origin backend, credentials (cookies) enabled.
export const api = axios.create({
  baseURL: '/api',
  withCredentials: true,
  timeout: 30000,
});

export const SESSION_EXPIRED_EVENT = 'tokenrouter:session-expired';

const SESSION_REFRESH_MARKER = '__tokenRouterSessionRefresh';
const SESSION_REFRESH_GENERATION = '__tokenRouterSessionGeneration';
type SessionRefreshMarker = 'skip' | 'refresh' | 'retried';
type SessionAwareRequestConfig<D = unknown> = AxiosRequestConfig<D> & {
  [SESSION_REFRESH_MARKER]?: SessionRefreshMarker;
  [SESSION_REFRESH_GENERATION]?: number;
};
type InternalSessionAwareRequestConfig<D = unknown> = InternalAxiosRequestConfig<D> & {
  [SESSION_REFRESH_MARKER]?: SessionRefreshMarker;
  [SESSION_REFRESH_GENERATION]?: number;
};

type RefreshOutcome = 'refreshed' | 'terminal' | 'transient';
let pendingSessionRefresh: Promise<RefreshOutcome> | null = null;
let sessionRefreshGeneration = 0;
const transientSessionRefreshFailures = new WeakSet<object>();

/**
 * Mark anonymous authentication requests so an expected 401 (for example, a
 * rejected password) cannot rotate or invalidate an unrelated live session.
 */
export function withoutSessionRefresh<D = unknown>(
  config: AxiosRequestConfig<D> = {},
): AxiosRequestConfig<D> {
  return {
    ...config,
    [SESSION_REFRESH_MARKER]: 'skip',
  } as SessionAwareRequestConfig<D>;
}

function marker(config: InternalAxiosRequestConfig | undefined): SessionRefreshMarker | undefined {
  return (config as InternalSessionAwareRequestConfig | undefined)?.[SESSION_REFRESH_MARKER];
}

function requestGeneration(config: InternalAxiosRequestConfig | undefined): number | undefined {
  return (config as InternalSessionAwareRequestConfig | undefined)?.[SESSION_REFRESH_GENERATION];
}

function isRefreshPath(url: string | undefined): boolean {
  if (!url) return false;
  const path = url.split(/[?#]/u, 1)[0]?.replace(/\/$/u, '');
  return path === '/user/auth/refresh' || path === '/api/user/auth/refresh';
}

function emitSessionExpired(): void {
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
  }
}

function refreshFailureCode(error: unknown): string | undefined {
  if (!axios.isAxiosError(error)) return undefined;
  const data = error.response?.data;
  if (!data || typeof data !== 'object' || Array.isArray(data)) return undefined;
  const code = (data as Record<string, unknown>).code;
  return typeof code === 'string' ? code : undefined;
}

function isTerminalRefreshFailure(error: unknown): boolean {
  if (!axios.isAxiosError(error)) return false;
  const status = error.response?.status;
  const code = refreshFailureCode(error);
  if (code === 'AUTH_SESSION_REVOKED'
    || code === 'AUTH_REFRESH_REPLAY'
    || code === 'AUTH_REFRESH_INVALID') return true;
  // The current backend's refresh-race response has no machine code. Keep
  // both it and a future coded race recoverable without clearing identity.
  if (status === 409 && (code === undefined || code === 'AUTH_REFRESH_RACE')) return false;
  if (status === 429 || status === undefined || status >= 500) return false;
  return status >= 400 && status < 500;
}

function markTransientSessionRefreshFailure(error: unknown): void {
  if (error && typeof error === 'object') transientSessionRefreshFailures.add(error);
}

export function isTransientSessionRefreshFailure(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && transientSessionRefreshFailures.has(error));
}

async function performSessionRefresh(): Promise<RefreshOutcome> {
  try {
    const response = await api.post<ApiResponse<unknown>>('/user/auth/refresh', undefined, {
      withCredentials: true,
      [SESSION_REFRESH_MARKER]: 'refresh',
    } as SessionAwareRequestConfig);
    if (!response.data || response.data.success !== true) {
      emitSessionExpired();
      return 'terminal';
    }
    sessionRefreshGeneration = sessionRefreshGeneration >= Number.MAX_SAFE_INTEGER
      ? 0
      : sessionRefreshGeneration + 1;
    return 'refreshed';
  } catch (error) {
    if (isTerminalRefreshFailure(error)) {
      emitSessionExpired();
      return 'terminal';
    }
    return 'transient';
  }
}

function refreshSession(): Promise<RefreshOutcome> {
  if (!pendingSessionRefresh) {
    const refresh = performSessionRefresh();
    pendingSessionRefresh = refresh;
    void refresh.then(() => {
      if (pendingSessionRefresh === refresh) pendingSessionRefresh = null;
    });
  }
  return pendingSessionRefresh;
}

function cancellationError(): Error {
  return new axios.CanceledError('Request canceled');
}

function waitForRefresh(
  refresh: Promise<RefreshOutcome>,
  signal: GenericAbortSignal | undefined,
): Promise<RefreshOutcome> {
  if (!signal) return refresh;
  if (signal.aborted) return Promise.reject(cancellationError());
  if (!signal.addEventListener || !signal.removeEventListener) return refresh;
  const eventSignal = signal as AbortSignal;
  return new Promise((resolve, reject) => {
    const canceled = () => reject(cancellationError());
    eventSignal.addEventListener('abort', canceled, { once: true });
    void refresh.then(
      (outcome) => {
        eventSignal.removeEventListener('abort', canceled);
        if (signal.aborted) reject(cancellationError());
        else resolve(outcome);
      },
      (error: unknown) => {
        eventSignal.removeEventListener('abort', canceled);
        reject(error);
      },
    );
  });
}

// Recover an expired access cookie through the HttpOnly refresh cookie. One
// browser-tab refresh is shared by all waiting requests and each request is
// replayed at most once. Only a definitive refresh rejection invalidates the
// in-memory identity; network/server failures leave it intact for recovery.
api.interceptors.request.use((config) => {
  const sessionConfig = config as InternalSessionAwareRequestConfig;
  if (sessionConfig[SESSION_REFRESH_GENERATION] === undefined) {
    sessionConfig[SESSION_REFRESH_GENERATION] = sessionRefreshGeneration;
  }
  return config;
});

api.interceptors.response.use(
  (response) => response,
  async (error: unknown) => {
    if (!axios.isAxiosError(error) || error.response?.status !== 401) {
      throw error;
    }

    const config = error.config;
    const requestMarker = marker(config);
    if (!config || requestMarker === 'skip' || requestMarker === 'refresh' || isRefreshPath(config.url)) {
      throw error;
    }
    if (requestMarker === 'retried') {
      emitSessionExpired();
      throw error;
    }
    if (config.signal?.aborted) throw cancellationError();

    // A slower request may return its stale 401 after another request already
    // refreshed the shared cookie. Replay it directly instead of rotating the
    // refresh token a second time.
    if (requestGeneration(config) !== undefined
      && requestGeneration(config) !== sessionRefreshGeneration) {
      const retryConfig = {
        ...config,
        [SESSION_REFRESH_MARKER]: 'retried',
        [SESSION_REFRESH_GENERATION]: sessionRefreshGeneration,
      } as InternalSessionAwareRequestConfig;
      return api.request(retryConfig);
    }

    const outcome = await waitForRefresh(refreshSession(), config.signal);
    if (outcome !== 'refreshed') {
      if (outcome === 'transient') markTransientSessionRefreshFailure(error);
      throw error;
    }
    if (config.signal?.aborted) throw cancellationError();

    const retryConfig = {
      ...config,
      [SESSION_REFRESH_MARKER]: 'retried',
      [SESSION_REFRESH_GENERATION]: sessionRefreshGeneration,
    } as InternalSessionAwareRequestConfig;
    return api.request(retryConfig);
  },
);

// Extract data payload from an envelope, throwing on failure.
export async function getData<T>(url: string, params?: unknown): Promise<T> {
  const res = await api.get<ApiResponse<T>>(url, { params });
  if (!res.data.success) {
    throw new Error(res.data.message || 'Request failed');
  }
  return res.data.data;
}

export async function postData<T>(
  url: string,
  body?: unknown,
  params?: Record<string, string | number | boolean>,
): Promise<T> {
  const res = params === undefined
    ? await api.post<ApiResponse<T>>(url, body)
    : await api.post<ApiResponse<T>>(url, body, { params });
  if (!res.data.success) {
    throw new Error(res.data.message || 'Request failed');
  }
  return res.data.data;
}

export async function putData<T>(url: string, body?: unknown): Promise<T> {
  const res = await api.put<ApiResponse<T>>(url, body);
  if (!res.data.success) {
    throw new Error(res.data.message || 'Request failed');
  }
  return res.data.data;
}
