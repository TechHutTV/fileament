import { Component, type ReactNode } from 'react';

export class ViewerBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    return this.state.failed
      ? <div className="static-thumb"><p role="alert">The 3D view could not be loaded. Downloads are still available.</p></div>
      : this.props.children;
  }
}
