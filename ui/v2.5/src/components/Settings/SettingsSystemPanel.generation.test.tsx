import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { IntlProvider } from "react-intl";
import { beforeEach, describe, expect, it, vi } from "vitest";
import locale from "src/locales/en-GB.json";
import { IntelGenerationSettings } from "./SettingsSystemPanel";

const mocks = vi.hoisted(() => ({
  save: vi.fn(),
  persisted: {} as Record<string, unknown>,
}));
vi.mock("src/core/generated-graphql", async (importOriginal) => ({
  ...(await importOriginal<typeof import("src/core/generated-graphql")>()),
  useConfigurationQuery: () => ({
    data: { configuration: { general: mocks.persisted } },
  }),
  useConfigureGeneralMutation: () => [mocks.save, { loading: false }],
}));
vi.mock("./context", () => ({
  useSettings: () => ({ advancedMode: false }),
  useSettingsOptional: () => ({ advancedMode: false }),
}));
function flatten(obj: object, prefix = ""): Record<string, string> {
  return Object.fromEntries(
    Object.entries(obj).flatMap(([key, value]) => {
      const path = prefix ? `${prefix}.${key}` : key;
      return typeof value === "string"
        ? [[path, value]]
        : Object.entries(flatten(value, path));
    })
  );
}
function panel() {
  return (
    <IntlProvider locale="en-GB" messages={flatten(locale)}>
      <IntelGenerationSettings />
    </IntlProvider>
  );
}
function edit(id: string, value: string) {
  fireEvent.click(
    within(document.getElementById(id)!).getByRole("button", { name: "Edit" })
  );
  fireEvent.change(screen.getByRole("textbox"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
}
beforeEach(() => {
  mocks.save.mockReset();
  mocks.save.mockResolvedValue({});
  mocks.persisted = {
    generationMarkerBackend: "software",
    generationSpriteBackend: "software",
    generationDevice: "/dev/dri/renderD128",
    generationBudgetEnabled: false,
    generationMaxProcesses: 0,
    generationMaxGPUProcesses: 0,
    generationThreads: 0,
    generationRestartRequired: false,
    generationConfigurationError: null,
    activeGeneration: {
      markerBackend: "software",
      spriteBackend: "software",
      device: "/dev/dri/renderD128",
      budgetEnabled: false,
      maxProcesses: 0,
      maxGPUProcesses: 0,
      threads: 0,
    },
  };
});
describe("Intel generation settings rendered controls", () => {
  it("renders software defaults, valid headings and separate running/saved labels", () => {
    render(panel());
    expect(
      screen.getByRole("heading", {
        name: "FFmpeg generation (experimental Intel)",
      })
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Running configuration: markers software, sprites software/
      )
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Saved requests: markers software, sprites software/)
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole("combobox").map((v) => (v as HTMLSelectElement).value)
    ).toEqual(["software", "software"]);
    const qsvOptions = screen.getAllByRole("option", { name: /Intel QSV/ });
    expect(qsvOptions[0]).toBeDisabled();
    expect(qsvOptions[1]).not.toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
  });
  it("keeps drafts distinct from saved/running values and submits one complete request", async () => {
    render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    expect(
      screen.getByText(/Saved requests: markers software/)
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Running configuration: markers software/)
    ).toBeInTheDocument();
    expect(mocks.save).not.toHaveBeenCalled();
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toEqual({
      generationMarkerBackend: "vaapi",
      generationSpriteBackend: "software",
      generationDevice: "/dev/dri/renderD128",
      generationBudgetEnabled: false,
      generationMaxProcesses: 0,
      generationMaxGPUProcesses: 0,
      generationThreads: 0,
    });
  });
  it("preserves dirty generation drafts when unrelated general settings refetch", async () => {
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    edit("generation-device", "/dev/dri/renderD129");
    mocks.persisted = { ...mocks.persisted, logLevel: "Debug" };
    rerender(panel());
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("vaapi");
    expect(
      within(document.getElementById("generation-device")!).getByText(
        "/dev/dri/renderD129"
      )
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Saved requests: markers software/)
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toMatchObject({
      generationMarkerBackend: "vaapi",
      generationDevice: "/dev/dri/renderD129",
    });
  });
  it("rejects invalid device before mutation and renders correction", () => {
    render(panel());
    edit("generation-device", "relative");
    expect(
      screen.getByText(
        "Choose an absolute render device such as /dev/dri/renderD128."
      )
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
    expect(mocks.save).not.toHaveBeenCalled();
  });
  it("rejects fractional limits and coupled GPU limits through real number inputs", () => {
    render(panel());
    fireEvent.click(
      within(document.getElementById("generationThreads")!).getByRole(
        "button",
        { name: "Edit" }
      )
    );
    fireEvent.change(screen.getByRole("spinbutton"), {
      target: { value: "1.5" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(
      screen.getByText(
        "Generation limits must be whole numbers between 0 (auto) and 64."
      )
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
    expect(mocks.save).not.toHaveBeenCalled();
    fireEvent.click(
      within(document.getElementById("generationThreads")!).getByRole(
        "button",
        { name: "Edit" }
      )
    );
    fireEvent.change(screen.getByRole("spinbutton"), {
      target: { value: "0" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    fireEvent.click(
      within(document.getElementById("generationMaxGPUProcesses")!).getByRole(
        "button",
        { name: "Edit" }
      )
    );
    fireEvent.change(screen.getByRole("spinbutton"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(
      screen.getByText(
        "GPU processes cannot exceed total processes (auto is 1)."
      )
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
  });
  it("shows server-confirmed restart state and supports explicit software rollback", async () => {
    mocks.persisted = {
      ...mocks.persisted,
      generationMarkerBackend: "qsv",
      generationSpriteBackend: "vaapi",
      generationRestartRequired: true,
    };
    render(panel());
    expect(
      screen.getByText(
        /Saved generation requests differ from the running configuration/
      )
    ).toBeInTheDocument();
    expect(
      screen.getByText(/QSV marker quality has not passed validation/)
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Running configuration: markers software, sprites software/
      )
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Select software rollback" })
    );
    expect(mocks.save).not.toHaveBeenCalled();
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toMatchObject({
      generationMarkerBackend: "software",
      generationSpriteBackend: "software",
      generationBudgetEnabled: false,
    });
  });
  it("shows save errors without claiming requests are persisted", async () => {
    mocks.save.mockRejectedValue(
      new Error("cannot set overridden value: generation.device")
    );
    render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[1], {
      target: { value: "qsv" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    expect(
      await screen.findByText("cannot set overridden value: generation.device")
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Saved requests: markers software, sprites software/)
    ).toBeInTheDocument();
  });
});
