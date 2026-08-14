import axios from 'axios';

// Shared axios instance: same-origin backend, credentials (cookies) enabled.
export const api = axios.create({
  baseURL: '/api',
  withCredentials: true,
  timeout: 30000,
});

// Type the standard response envelope.
export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data: T;
}

export interface User {
  id: number;
  username: string;
  display_name: string;
  role: number;
  group: string;
  quota: number;
  used_quota: number;
  request_count: number;
  email?: string;
}

export interface Token {
  id: number;
  key: string;
  name: string;
  status: number;
  remain_quota: number;
  unlimited_quota: boolean;
  created_time: number;
  expired_time: number;
  used_quota: number;
}

// Extract data payload from an envelope, throwing on failure.
export async function getData<T>(url: string, params?: unknown): Promise<T> {
  const res = await api.get<ApiResponse<T>>(url, { params });
  if (!res.data.success) {
    throw new Error(res.data.message || 'Request failed');
  }
  return res.data.data;
}

export async function postData<T>(url: string, body?: unknown): Promise<T> {
  const res = await api.post<ApiResponse<T>>(url, body);
  if (!res.data.success) {
    throw new Error(res.data.message || 'Request failed');
  }
  return res.data.data;
}
