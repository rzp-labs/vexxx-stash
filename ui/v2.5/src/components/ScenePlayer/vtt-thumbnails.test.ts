import videojs, { VideoJsPlayer } from "video.js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "./vtt-thumbnails";

const vtt = `WEBVTT

00:00:01.000 --> 00:00:02.000
sprite.jpg#xywh=0,0,100,50

00:00:02.000 --> 00:00:03.000
sprite.jpg#xywh=100,0,120,50
`;

let player: VideoJsPlayer;
let vttResponse: string;

beforeEach(() => {
  vttResponse = vtt;
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation(function (
    this: XMLHttpRequest
  ) {
    Object.defineProperty(this, "responseText", { value: vttResponse });
    this.dispatchEvent(new Event("load"));
  });
  const element = document.createElement("video");
  document.body.appendChild(element);
  player = videojs(element, {
    controls: true,
    plugins: { vttThumbnails: { showTimestamp: true } },
  });
  vi.spyOn(player, "duration").mockReturnValue(10);
});

afterEach(() => {
  player.dispose();
  vi.restoreAllMocks();
});

async function timeline() {
  await new Promise<void>((resolve) => player.ready(resolve));
  player.vttThumbnails().src("/sprites.vtt");
  await vi.waitFor(() =>
    expect(player.$(".vjs-vtt-thumbnail-display")).not.toBeNull()
  );
  const progress = player.$(".vjs-progress-control") as HTMLElement;
  const seek = player.$(".vjs-progress-holder") as HTMLElement;
  for (const [el, left, width] of [
    [progress, 0, 1000],
    [seek, 100, 800],
  ] as const) {
    Object.defineProperty(el, "offsetWidth", { value: width });
    Object.defineProperty(el, "offsetParent", { value: document.body });
    Object.defineProperty(el, "offsetLeft", { value: left });
    vi.spyOn(el, "getBoundingClientRect").mockReturnValue({
      left,
      top: 0,
      width,
      height: 10,
      right: left + width,
      bottom: 10,
      x: left,
      y: 0,
      toJSON: () => ({}),
    });
  }
  const holder = player.$(".vjs-vtt-thumbnail-display") as HTMLElement;
  const display = player
    .getChild("controlBar")!
    .getChild("progressControl")!
    .getChild("seekBar")!
    .getChild("mouseTimeDisplay") as videojs.MouseTimeDisplay;
  const timeTooltip = display.getChild("timeTooltip") as videojs.TimeToolTip & {
    updateTime: (
      rect: ClientRect,
      percent: number,
      time: number,
      callback: () => void
    ) => void;
  };
  const tooltip = vi.spyOn(timeTooltip, "updateTime");
  return {
    holder,
    tooltip,
    hover: (percent: number) => {
      const event = new MouseEvent("pointermove", {
        clientX: 100 + 800 * percent,
      });
      progress.dispatchEvent(new MouseEvent("pointerover"));
      progress.dispatchEvent(event);
      display.update(seek.getBoundingClientRect(), percent);
    },
  };
}

describe("virtual timeline hover", () => {
  it("keeps an interior virtual endpoint on the preceding cue", async () => {
    vttResponse += `
00:00:03.000 --> 00:00:04.000
sprite.jpg#xywh=220,0,140,50
`;
    player.virtualStart = 1;
    player.virtualEnd = 3;
    const bar = await timeline();
    bar.hover(1);
    expect(bar.tooltip.mock.lastCall?.[2]).toBe(3);
    expect(bar.holder.style.opacity).toBe("1");
    expect(bar.holder.style.width).toBe("120px");

    delete player.virtualStart;
    delete player.virtualEnd;
    bar.hover(0.3);
    expect(bar.holder.style.width).toBe("140px");
  });

  it("uses the containing cue when no cue ends at the virtual endpoint", async () => {
    player.virtualStart = 1;
    player.virtualEnd = 2.5;
    const bar = await timeline();
    bar.hover(1);
    expect(bar.tooltip.mock.lastCall?.[2]).toBe(2.5);
    expect(bar.holder.style.opacity).toBe("1");
    expect(bar.holder.style.width).toBe("120px");
  });

  it.each([
    [0, 1, "100px"],
    [0.5, 2, "120px"],
    [0.997, 2.994, "120px"],
    [1, 3, "120px"],
  ])(
    "maps hover %s to absolute time %s and the matching cue",
    async (percent, time, tile) => {
      player.virtualStart = 1;
      player.virtualEnd = 3;
      const bar = await timeline();
      bar.hover(percent);
      expect(bar.tooltip).toHaveBeenLastCalledWith(
        expect.anything(),
        percent,
        expect.any(Number),
        expect.any(Function)
      );
      expect(bar.tooltip.mock.lastCall?.[2]).toBeCloseTo(time, 8);
      expect(bar.holder.style.opacity).toBe("1");
      expect(bar.holder.style.width).toBe(tile);
    }
  );

  it("keeps full-file hover mapped to physical duration", async () => {
    const bar = await timeline();
    bar.hover(0.2);
    expect(bar.tooltip).toHaveBeenLastCalledWith(
      expect.anything(),
      0.2,
      2,
      expect.any(Function)
    );
    expect(bar.holder.style.opacity).toBe("1");
    expect(bar.holder.style.width).toBe("120px");
    bar.hover(0.9);
    expect(bar.holder.style.opacity).toBe("0");
  });
});
