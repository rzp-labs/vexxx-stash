import React from "react";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IntlProvider } from "react-intl";
import posthog from "posthog-js/no-external";
import { ErrorBoundary } from "./ErrorBoundary";

vi.mock("posthog-js/no-external", () => ({
  default: { __loaded: false, captureException: vi.fn() },
}));

function FailingApp(): React.ReactElement {
  throw new Error("Application initialization failed");
}

describe("ErrorBoundary fallback", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    posthog.__loaded = false;
  });

  it("renders an initialization failure outside the translation provider", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <ErrorBoundary>
        <FailingApp />
      </ErrorBoundary>
    );
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "Something went wrong"
    );
    expect(
      screen.getByText("Application initialization failed", { exact: false })
    ).toBeTruthy();
  });

  it("attaches the render operation and React component context to captured errors", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    posthog.__loaded = true;
    render(
      <ErrorBoundary>
        <FailingApp />
      </ErrorBoundary>
    );
    expect(posthog.captureException).toHaveBeenCalledWith(expect.any(Error), {
      operation: "react.render",
      component: "ErrorBoundary",
      context: { component_stack: expect.stringContaining("FailingApp") },
    });
  });

  it("retains translated errors when a provider is available", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <IntlProvider
        locale="en"
        messages={{ "errors.something_went_wrong": "Localized failure" }}
      >
        <ErrorBoundary>
          <FailingApp />
        </ErrorBoundary>
      </IntlProvider>
    );
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "Localized failure"
    );
  });
});
