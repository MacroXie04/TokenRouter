
export interface ChannelAdminViewProps {
  canRead: boolean;
  canOperate: boolean;
  canWrite: boolean;
  canSensitiveWrite: boolean;
  isRoot?: boolean;
}
