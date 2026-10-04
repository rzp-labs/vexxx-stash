import { ApolloProvider } from "@apollo/client";
import ReactDOM from "react-dom";
import { BrowserRouter } from "react-router-dom";
import posthog from "posthog-js";
import { App } from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { getClient } from "./core/StashService";
import { baseURL, getPlatformURL } from "./core/createClient";
import "./index.css";
import "./index.scss";
import * as serviceWorker from "./serviceWorker";

const posthogProjectToken = import.meta.env.VITE_PUBLIC_POSTHOG_PROJECT_TOKEN;
const posthogHost = import.meta.env.VITE_PUBLIC_POSTHOG_HOST;

if (!posthogProjectToken || !posthogHost) {
  if (import.meta.env.DEV) {
    const missingVariable = posthogProjectToken
      ? "VITE_PUBLIC_POSTHOG_HOST"
      : "VITE_PUBLIC_POSTHOG_PROJECT_TOKEN";
    throw new Error(
      `${missingVariable} variable required by PostHog is missing or un-configured, this causes events to be silently missed. This error stops appearing once ${missingVariable} is configured`
    );
  }
} else {
  posthog.init(posthogProjectToken, {
    api_host: posthogHost,
    defaults: "2026-01-30",
    capture_pageview: "history_change",
    capture_exceptions: true,
  });
}

ReactDOM.render(
  <>
    <link
      rel="stylesheet"
      type="text/css"
      href={getPlatformURL("css").toString()}
    />
    <ErrorBoundary>
      <BrowserRouter basename={baseURL}>
        <ApolloProvider client={getClient()}>
          <App />
        </ApolloProvider>
      </BrowserRouter>
    </ErrorBoundary>
  </>,
  document.getElementById("root")
);

const script = document.createElement("script");
script.src = getPlatformURL("javascript").toString();
document.body.appendChild(script);

// If you want your app to work offline and load faster, you can change
// unregister() to register() below. Note this comes with some pitfalls.
// Learn more about service workers: http://bit.ly/CRA-PWA
serviceWorker.unregister();
