import { Component, type ErrorInfo, type ReactNode } from 'react';

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: boolean;
}

/**
 * Keeps a rendering failure in one part of the app from taking down the
 * whole page. The fallback offers a retry and a way back to a known-good
 * screen instead of leaving a blank page.
 */
export default class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Keep the detail in the console for diagnosis; the user only needs a
    // recoverable message.
    console.error('Unhandled error in Emissary UI', error, info.componentStack);
  }

  private retry = () => {
    this.setState({ failed: false });
  };

  render() {
    if (this.state.failed) {
      return (
        <div className="error-boundary" role="alert">
          <h2>Something went wrong</h2>
          <p>This screen could not be displayed. Your messages and settings are safe.</p>
          <div className="error-boundary-actions">
            <button type="button" className="btn btn-primary" onClick={this.retry}>
              Try again
            </button>
            {/* A plain link works even when the failure happened above the router. */}
            <a className="btn btn-secondary" href="/">Back to inbox</a>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
}
