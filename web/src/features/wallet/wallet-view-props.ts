import type { User } from '../../shared/api/client';
import { type TurnstileConfig } from "../security";

export interface WalletViewProps {
  user: User;
  onNavigate: (target: string) => void;
  onUserChange?: (user: User) => void;
  onLogout: () => void;
  turnstileConfig?: TurnstileConfig;
  initialShowHistory?: boolean;
}
