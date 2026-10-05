import React from "react";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IntlProvider } from "react-intl";
import { ErrorBoundary } from "./ErrorBoundary";

vi.mock("posthog-js/no-external", () => ({
  default: { __loaded: false, captureException: vi.fn() },
}));

function FailingApp(): React.ReactElement {
  throw new Error("Application initialization failed");
}

describe("ErrorBoundary fallback", () => {
  afterEach(() => vi.restoreAllMocks());

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
