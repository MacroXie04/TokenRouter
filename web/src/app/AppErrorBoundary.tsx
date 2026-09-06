import { Component, type ErrorInfo, type ReactNode } from 'react';
import { ErrorView } from '../features/errors/ErrorView';

interface BoundaryState { failed: boolean }

export class AppErrorBoundary extends Component<{ children: ReactNode }, BoundaryState> {
  state: BoundaryState = { failed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('TokenRouter frontend error', error, info.componentStack);
  }

  render() {
    if (this.state.failed) return <ErrorView code="500" />;
    return this.props.children;
  }
}
