import React from "react";
import { IntlProvider } from "react-intl";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import posthogClient from "posthog-js/no-external";
import { SettingsLocalHTTPSPanel } from "./SettingsLocalHTTPSPanel";

vi.mock("posthog-js/no-external", () => ({
  default: { captureException: vi.fn() },
}));
vi.mock("./SettingSection", () => ({
  SettingSection: ({ children }: { children: React.ReactNode }) => (
    <section>{children}</section>
  ),
}));
vi.mock("./Inputs", () => ({
  Setting: ({ children }: { children: React.ReactNode }) => (
    <div>{children}</div>
  ),
}));

const url = "https://192.0.2.1:9443";
const otherURL = "https://192.0.2.2:9443";
const status = {
  enabled: true,
  running: true,
  port: 9443,
  urls: [url, otherURL],
};
const copyButton = (value = url) =>
  screen.getByRole("button", { name: `Copy URL ${value}` });
const selectButton = (value = url) =>
  screen.getByRole("button", { name: `Select URL ${value}` });
function deferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}
async function panel(
  clipboard: unknown = { writeText: vi.fn().mockResolvedValue(undefined) }
) {
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: clipboard,
  });
  const view = render(
    <IntlProvider locale="en" messages={{ "loading.generic": "Loading…" }}>
      <SettingsLocalHTTPSPanel />
    </IntlProvider>
  );
  await screen.findAllByRole("textbox", { name: "Headset URL" });
  return view;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => status })
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(navigator, "clipboard");
});

describe("Local HTTPS URL copying", () => {
  it.each([undefined, null, {}, { writeText: undefined }])(
    "offers selected manual copying when clipboard is unavailable: %j",
    async (clipboard) => {
      // Avoid the helper's default clipboard when explicitly testing undefined.
      await panel(null);
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: clipboard,
      });
      fireEvent.click(copyButton());
      expect(
        await screen.findByText(/Automatic copying is unavailable/)
      ).toBeInTheDocument();
      const input = screen.getAllByRole("textbox", {
        name: "Headset URL",
      })[0] as HTMLInputElement;
      expect(input).toHaveFocus();
      expect(input.selectionStart).toBe(0);
      expect(input.selectionEnd).toBe(url.length);
      expect(input).toHaveAttribute("readonly");
      expect(
        screen.queryByText("URL copied to clipboard.")
      ).not.toBeInTheDocument();
      expect(posthogClient.captureException).not.toHaveBeenCalled();
    }
  );

  it("selects the correct read-only URL using the explicit manual button and keyboard focus", async () => {
    await panel();
    fireEvent.click(selectButton(otherURL));
    const inputs = screen.getAllByRole("textbox", {
      name: "Headset URL",
    }) as HTMLInputElement[];
    expect(inputs[1]).toHaveFocus();
    expect(inputs[1].selectionEnd).toBe(otherURL.length);
    inputs[0].focus();
    expect(inputs[0].selectionEnd).toBe(url.length);
    expect(screen.getAllByText(/Ctrl\+C or ⌘C/)).toHaveLength(2);
  });

  it("only reports success after resolution, prevents duplicate clicks and supports copying another URL", async () => {
    const first = deferred();
    const writeText = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue(undefined);
    const clipboard = { writeText };
    await panel(clipboard);
    fireEvent.click(copyButton());
    fireEvent.click(copyButton());
    expect(writeText).toHaveBeenCalledExactlyOnceWith(url);
    expect(writeText.mock.contexts[0]).toBe(clipboard);
    expect(copyButton()).toBeDisabled();
    expect(
      screen.queryByText("URL copied to clipboard.")
    ).not.toBeInTheDocument();
    fireEvent.click(copyButton(otherURL));
    await waitFor(() =>
      expect(screen.getAllByText("URL copied to clipboard.")).toHaveLength(1)
    );
    await act(async () => first.resolve());
    expect(screen.getAllByText("URL copied to clipboard.")).toHaveLength(2);
    expect(copyButton()).not.toBeDisabled();
    fireEvent.click(copyButton());
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(3));
  });

  it("shows denied access honestly, keeps manual copying usable and clears failure on retry", async () => {
    const writeText = vi
      .fn()
      .mockRejectedValueOnce(new DOMException("denied", "NotAllowedError"))
      .mockResolvedValue(undefined);
    await panel({ writeText });
    fireEvent.click(copyButton());
    expect(
      await screen.findByText(/Clipboard access was denied/)
    ).toBeInTheDocument();
    expect(
      screen.queryByText("URL copied to clipboard.")
    ).not.toBeInTheDocument();
    expect(copyButton()).not.toBeDisabled();
    fireEvent.click(selectButton());
    expect(
      screen.getAllByRole("textbox", { name: "Headset URL" })[0]
    ).toHaveFocus();
    expect(posthogClient.captureException).not.toHaveBeenCalled();
    fireEvent.click(copyButton());
    expect(
      await screen.findByText("URL copied to clipboard.")
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Clipboard access was denied/)
    ).not.toBeInTheDocument();
  });

  it.each(["sync", "async"])(
    "reports a real %s operation failure without claiming success",
    async (mode) => {
      const failure = new Error("clipboard service failed");
      const writeText = vi.fn(() => {
        if (mode === "sync") throw failure;
        return Promise.reject(failure);
      });
      vi.spyOn(console, "error").mockImplementation(() => {});
      await panel({ writeText });
      fireEvent.click(copyButton());
      expect(
        await screen.findByText(
          /Could not copy the URL: clipboard service failed/
        )
      ).toBeInTheDocument();
      expect(posthogClient.captureException).toHaveBeenCalledExactlyOnceWith(
        failure,
        {
          operation: "clipboard.write_text",
          component: "SettingsLocalHTTPSPanel",
        }
      );
      expect(copyButton()).not.toBeDisabled();
      expect(
        screen.queryByText("URL copied to clipboard.")
      ).not.toBeInTheDocument();
    }
  );

  it("does not let a telemetry failure bypass manual fallback", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.mocked(posthogClient.captureException).mockImplementationOnce(() => {
      throw new Error("telemetry unavailable");
    });
    await panel({
      writeText: vi.fn().mockRejectedValue(new Error("clipboard failed")),
    });
    fireEvent.click(copyButton());
    expect(
      await screen.findByText(/Could not copy the URL: clipboard failed/)
    ).toBeInTheDocument();
    expect(console.error).toHaveBeenCalledWith(
      "Could not report clipboard error",
      expect.any(Error)
    );
  });

  it.each(["resolve", "reject"])(
    "ignores late %s after disable/re-enable removes the old URL row",
    async (outcome) => {
      const request = deferred();
      const writeText = vi
        .fn()
        .mockReturnValueOnce(request.promise)
        .mockResolvedValue(undefined);
      await panel({ writeText });
      fireEvent.click(copyButton());
      vi.mocked(fetch).mockResolvedValueOnce({
        ok: true,
        json: async () => ({ ...status, running: false }),
      } as Response);
      fireEvent.click(screen.getByRole("button", { name: "Disable" }));
      await waitFor(() =>
        expect(
          screen.queryByRole("textbox", { name: "Headset URL" })
        ).not.toBeInTheDocument()
      );
      fireEvent.click(screen.getByRole("button", { name: "Enable" }));
      await screen.findAllByRole("textbox", { name: "Headset URL" });
      await act(async () => {
        if (outcome === "resolve") request.resolve();
        else request.reject(new DOMException("denied", "NotAllowedError"));
      });
      expect(
        screen.queryByText("URL copied to clipboard.")
      ).not.toBeInTheDocument();
      expect(
        screen.queryByText(/Clipboard access was denied/)
      ).not.toBeInTheDocument();
      expect(copyButton()).not.toBeDisabled();
      fireEvent.click(copyButton());
      expect(
        await screen.findByText("URL copied to clipboard.")
      ).toBeInTheDocument();
    }
  );

  it("ignores late completion after navigation unmounts the panel", async () => {
    const request = deferred();
    const view = await panel({
      writeText: vi.fn().mockReturnValueOnce(request.promise),
    });
    fireEvent.click(copyButton());
    view.unmount();
    await act(async () => request.resolve());
    expect(
      screen.queryByText("URL copied to clipboard.")
    ).not.toBeInTheDocument();
  });

  it("clears old feedback when refresh replaces the URL", async () => {
    await panel();
    fireEvent.click(copyButton());
    await screen.findByText("URL copied to clipboard.");
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: true,
      json: async () => ({ ...status, urls: ["https://192.0.2.3:9443"] }),
    } as Response);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await screen.findByRole("button", {
      name: "Copy URL https://192.0.2.3:9443",
    });
    expect(
      screen.queryByText("URL copied to clipboard.")
    ).not.toBeInTheDocument();
  });
});
