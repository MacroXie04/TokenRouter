// Wire contracts shared by feature clients and application session state.
export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data: T;
}

export type AdminPermissionMatrix = Record<string, Record<string, boolean>>;

export interface UserPermissions {
  sidebar_settings?: unknown;
  sidebar_modules?: unknown;
  admin_permissions?: AdminPermissionMatrix;
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
  language?: unknown;
  setting?: unknown;
  sidebar_modules?: unknown;
  permissions?: UserPermissions;
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
