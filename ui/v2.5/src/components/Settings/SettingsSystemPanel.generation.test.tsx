import {
  act,
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
  readQuery: vi.fn(),
  persisted: {} as Record<string, unknown>,
}));
vi.mock("src/core/generated-graphql", async (importOriginal) => ({
  ...(await importOriginal<typeof import("src/core/generated-graphql")>()),
  useConfigurationQuery: () => ({
    data: { configuration: { general: mocks.persisted } },
    client: { readQuery: mocks.readQuery },
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
  mocks.readQuery.mockReset();
  mocks.readQuery.mockImplementation(() => ({
    configuration: { general: mocks.persisted },
  }));
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
  it("allows correction of invalid saved YAML using the displayed defaults", async () => {
    mocks.persisted = {
      ...mocks.persisted,
      generationConfigurationError:
        "generation.budget.threads must be an integer or auto",
      generationRestartRequired: true,
    };
    render(panel());
    const save = screen.getByRole("button", {
      name: "Save generation settings",
    });
    expect(save).not.toBeDisabled();
    expect(
      screen.getByText(/Correct and save these settings before restarting/)
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Restart the application to apply them/)
    ).not.toBeInTheDocument();
    fireEvent.click(save);
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toMatchObject({
      generationThreads: 0,
    });
  });
  it("offers a valid proposal when saved backends, device and limits are invalid", async () => {
    mocks.persisted = {
      ...mocks.persisted,
      generationMarkerBackend: "broken",
      generationSpriteBackend: "broken",
      generationDevice: "relative",
      generationMaxProcesses: 4,
      generationMaxGPUProcesses: 2,
      generationThreads: 65,
      generationConfigurationError: "invalid saved configuration",
    };
    render(panel());
    expect(
      screen.getAllByRole("combobox").map((v) => (v as HTMLSelectElement).value)
    ).toEqual(["software", "software"]);
    const save = screen.getByRole("button", {
      name: "Save generation settings",
    });
    expect(save).not.toBeDisabled();
    fireEvent.click(save);
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toEqual({
      generationMarkerBackend: "software",
      generationSpriteBackend: "software",
      generationDevice: "/dev/dri/renderD128",
      generationBudgetEnabled: false,
      generationMaxProcesses: 4,
      generationMaxGPUProcesses: 2,
      generationThreads: 0,
    });
  });
  it("merges external generation saves into pristine fields without erasing edits", async () => {
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    edit("generation-device", "/dev/dri/renderD129");
    mocks.persisted = {
      ...mocks.persisted,
      generationMarkerBackend: "qsv",
      generationSpriteBackend: "qsv",
      generationDevice: "/dev/dri/renderD130",
    };
    rerender(panel());
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("vaapi");
    expect(screen.getAllByRole("combobox")[1]).toHaveValue("qsv");
    expect(
      within(document.getElementById("generation-device")!).getByText(
        "/dev/dri/renderD129"
      )
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Your edited fields are preserved/)
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toMatchObject({
      generationMarkerBackend: "vaapi",
      generationSpriteBackend: "qsv",
      generationDevice: "/dev/dri/renderD129",
    });
  });
  it("reconciles its own successful save before following later external changes", async () => {
    mocks.save.mockImplementation(async ({ variables }) => {
      mocks.persisted = { ...mocks.persisted, ...variables.input };
      return { data: { configureGeneral: mocks.persisted } };
    });
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    edit("generation-device", "/dev/dri/renderD129");
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Save generation settings" })
      ).toBeDisabled()
    );
    mocks.persisted = {
      ...mocks.persisted,
      generationMarkerBackend: "qsv",
      generationDevice: "/dev/dri/renderD130",
    };
    rerender(panel());
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("qsv");
    expect(
      within(document.getElementById("generation-device")!).getByText(
        "/dev/dri/renderD130"
      )
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Your edited fields are preserved/)
    ).not.toBeInTheDocument();
  });
  it("can acknowledge an unsaved proposal when an external save matches it", async () => {
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    mocks.persisted = { ...mocks.persisted, generationMarkerBackend: "vaapi" };
    rerender(panel());
    const save = screen.getByRole("button", {
      name: "Save generation settings",
    });
    expect(save).not.toBeDisabled();
    fireEvent.click(save);
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
  });
  it("retains failed-save edits through an external refresh and retries the merged proposal", async () => {
    mocks.save.mockRejectedValueOnce(new Error("connection lost"));
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    expect(await screen.findByText("connection lost")).toBeInTheDocument();
    mocks.persisted = { ...mocks.persisted, generationSpriteBackend: "qsv" };
    rerender(panel());
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("vaapi");
    expect(screen.getAllByRole("combobox")[1]).toHaveValue("qsv");
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledTimes(2));
    expect(mocks.save.mock.calls[1][0].variables.input).toMatchObject({
      generationMarkerBackend: "vaapi",
      generationSpriteBackend: "qsv",
    });
    await waitFor(() =>
      expect(screen.queryByText("connection lost")).not.toBeInTheDocument()
    );
  });
  it("uses a newer refetch when the older mutation response completes later", async () => {
    let finish!: (value: unknown) => void;
    mocks.save.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const { rerender } = render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    const oldResponse = {
      ...mocks.persisted,
      generationMarkerBackend: "vaapi",
    };
    mocks.persisted = {
      ...mocks.persisted,
      generationMarkerBackend: "software",
      generationThreads: 4,
    };
    rerender(panel());
    await act(async () => finish({ data: { configureGeneral: oldResponse } }));
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("software");
    expect(
      within(document.getElementById("generationThreads")!).getByText("4")
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
    expect(
      screen.queryByText(/Your edited fields are preserved/)
    ).not.toBeInTheDocument();
  });
  it("preserves a readable GPU limit while requiring correction of an invalid total", async () => {
    mocks.persisted = {
      ...mocks.persisted,
      generationMaxProcesses: 0,
      generationMaxGPUProcesses: 2,
      generationThreads: 3,
      generationConfigurationError:
        "generation.budget.processes must be an integer or auto",
    };
    render(panel());
    expect(
      within(document.getElementById("generationMaxGPUProcesses")!).getByText(
        "2"
      )
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save generation settings" })
    ).toBeDisabled();
    fireEvent.click(
      within(document.getElementById("generationMaxProcesses")!).getByRole(
        "button",
        { name: "Edit" }
      )
    );
    fireEvent.change(screen.getByRole("spinbutton"), {
      target: { value: "4" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0].variables.input).toMatchObject({
      generationMaxProcesses: 4,
      generationMaxGPUProcesses: 2,
      generationThreads: 3,
    });
  });
  it("retains edits if the completed save cannot confirm the current query", async () => {
    mocks.readQuery.mockReturnValue(null);
    render(panel());
    fireEvent.change(screen.getAllByRole("combobox")[0], {
      target: { value: "vaapi" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save generation settings" })
    );
    expect(
      await screen.findByText(/current saved settings could not be confirmed/)
    ).toBeInTheDocument();
    expect(screen.getAllByRole("combobox")[0]).toHaveValue("vaapi");
    expect(
      screen.getByText(/Saved requests: markers software/)
    ).toBeInTheDocument();
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
