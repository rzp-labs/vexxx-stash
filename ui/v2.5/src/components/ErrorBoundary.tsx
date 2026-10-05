import React from "react";
import { FormattedMessage, IntlContext } from "react-intl";
import posthog from "posthog-js/no-external";
import { isLazyComponentError } from "src/utils/lazyComponent";

interface IErrorBoundaryProps {
  children?: React.ReactNode;
}

type ErrorInfo = {
  componentStack: string;
};

interface IErrorBoundaryState {
  error?: Error;
  errorHelpId?: string;
  errorInfo?: ErrorInfo;
}

export class ErrorBoundary extends React.Component<
  IErrorBoundaryProps,
  IErrorBoundaryState
> {
  static contextType = IntlContext;

  declare context: React.ContextType<typeof IntlContext>;

  constructor(props: IErrorBoundaryProps) {
    super(props);
    this.state = {};
  }

  public componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    let errorHelpId: string | undefined;
    if (isLazyComponentError(error)) {
      errorHelpId = "errors.lazy_component_error_help";
    }
    if (posthog.__loaded) {
      posthog.captureException(error);
    }
    this.setState({
      error,
      errorHelpId,
      errorInfo,
    });
  }

  public render() {
    const { error, errorHelpId, errorInfo } = this.state;
    if (errorInfo) {
      // Error path
      return (
        <div>
          <h2>
            {this.context ? (
              <FormattedMessage id="errors.something_went_wrong" />
            ) : (
              "Something went wrong"
            )}
          </h2>
          {errorHelpId && (
            <h5>
              {this.context ? (
                <FormattedMessage id={errorHelpId} />
              ) : (
                "Reload the page to load the latest application version."
              )}
            </h5>
          )}
          <details className="error-message">
            {error?.toString()}
            <br />
            {errorInfo.componentStack.trim().replaceAll(/^\s*/gm, "    ")}
          </details>
        </div>
      );
    }

    // Normally, just render children
    return this.props.children;
  }
}
