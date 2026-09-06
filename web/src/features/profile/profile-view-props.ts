import type { ProfileAccount } from './profile-api';
export interface ProfileViewProps {
  onNavigate?: (target: string) => void;
  onProfileChange?: (profile: ProfileAccount) => void;
  onExternalNavigate?: (target: string) => void;
}
