import videojs, { VideoJsPlayer } from "video.js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import MarkersPlugin, { IMarker } from "./markers";
import { watchMarkerMetadata } from "./markerLifecycle";

const marker: IMarker = {
  title: "Test marker",
  seconds: 10,
  end_seconds: 20,
  primaryTag: { name: "Test tag" },
};

let players: VideoJsPlayer[] = [];

function createPlayer() {
  const element = document.createElement("video");
  document.body.appendChild(element);
  const player = videojs(element, { controls: true });
  players.push(player);
  vi.spyOn(player, "duration").mockReturnValue(100);
  return player;
}

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
});

afterEach(() => {
  players.forEach((player) => {
    if (!player.isDisposed()) player.dispose();
  });
  players = [];
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("marker plugin lifetime", () => {
  function delayedPlugin(player: VideoJsPlayer) {
    let ready: (() => void) | undefined;
    vi.spyOn(player, "ready").mockImplementation((callback) => {
      ready = callback;
      return player;
    });
    expect(videojs.getPlugin("markers")).toBe(MarkersPlugin);
    return { plugin: player.markers(), ready: () => ready?.() };
  }

  it("ignores a ready callback after player disposal", () => {
    const player = createPlayer();
    const { plugin, ready } = delayedPlugin(player);
    player.dispose();
    expect(() => ready()).not.toThrow();
    expect(() => plugin.addDotMarkers([marker])).not.toThrow();
    expect(() => plugin.addRangeMarkers([marker])).not.toThrow();
    expect(document.querySelector(".vjs-marker-tooltip")).toBeNull();
  });

  it("ignores delayed ready after plugin disposal while the player is alive", () => {
    const player = createPlayer();
    const { plugin, ready } = delayedPlugin(player);
    plugin.dispose();
    expect(player.isDisposed()).toBe(false);
    expect(() => ready()).not.toThrow();
    expect(player.el().querySelector(".vjs-marker-tooltip")).toBeNull();
  });

  it("tolerates an unavailable player element", () => {
    const player = createPlayer();
    const { plugin, ready } = delayedPlugin(player);
    const el = vi.spyOn(player, "el").mockReturnValue(null!);
    expect(() => ready()).not.toThrow();
    expect(() => plugin.addDotMarkers([marker])).not.toThrow();
    expect(() => plugin.addRangeMarkers([marker])).not.toThrow();
    el.mockRestore();
  });

  it("keeps live dot/range rendering and tooltip behavior, and removes owned DOM on disposal", () => {
    const player = createPlayer();
    const { plugin, ready } = delayedPlugin(player);
    ready();
    plugin.addDotMarkers([marker]);
    plugin.addRangeMarkers([marker]);
    const dot = player.el().querySelector<HTMLElement>(".vjs-marker")!;
    const range = player.el().querySelector<HTMLElement>(".vjs-marker-range")!;
    const tooltip = player
      .el()
      .querySelector<HTMLElement>(".vjs-marker-tooltip")!;
    expect(dot.style.left).toContain("10%");
    expect(range.style.width).toContain("10%");
    const seek = vi.spyOn(player, "currentTime");
    dot.click();
    expect(seek).toHaveBeenCalledWith(10);
    range.dispatchEvent(new MouseEvent("mouseenter"));
    expect(tooltip.innerText).toBe(marker.title);
    expect(tooltip.style.visibility).toBe("visible");
    range.dispatchEvent(new MouseEvent("mouseout"));
    expect(tooltip.style.visibility).toBe("hidden");
    range.dispatchEvent(new MouseEvent("mouseenter"));
    plugin.clearMarkers();
    expect(tooltip.style.visibility).toBe("hidden");
    plugin.addDotMarkers([marker]);
    expect(player.el().querySelectorAll(".vjs-marker")).toHaveLength(1);
    plugin.dispose();
    expect(player.el().querySelector(".vjs-marker")).toBeNull();
    expect(player.el().querySelector(".vjs-marker-tooltip")).toBeNull();
  });
});

describe("marker metadata scheduling", () => {
  function frames() {
    let nextId = 0;
    const queued = new Map<number, FrameRequestCallback>();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      queued.set(++nextId, callback);
      return nextId;
    });
    const cancel = vi.fn((id: number) => queued.delete(id));
    vi.stubGlobal("cancelAnimationFrame", cancel);
    return {
      queued,
      cancel,
      flush: () => {
        const callbacks = [...queued.values()];
        queued.clear();
        callbacks.forEach((callback) => callback(0));
      },
    };
  }

  it("waits for metadata and coalesces repeated events", () => {
    const player = createPlayer();
    vi.spyOn(player, "readyState").mockReturnValue(0);
    const queue = frames();
    const render = vi.fn();
    const stop = watchMarkerMetadata(player, render);
    expect(queue.queued.size).toBe(0);
    player.trigger("loadedmetadata");
    player.trigger("loadedmetadata");
    expect(queue.queued.size).toBe(1);
    queue.flush();
    expect(render).toHaveBeenCalledTimes(1);
    player.trigger("loadedmetadata");
    queue.flush();
    expect(render).toHaveBeenCalledTimes(2);
    stop();
    player.trigger("loadedmetadata");
    expect(queue.queued.size).toBe(0);
  });

  it("cancels old scene work on navigation and renders only the next scene", () => {
    const player = createPlayer();
    vi.spyOn(player, "readyState").mockReturnValue(1);
    const queue = frames();
    const oldRender = vi.fn();
    const stopOld = watchMarkerMetadata(player, oldRender);
    const oldCallback = [...queue.queued.values()][0];
    stopOld();
    expect(queue.cancel).toHaveBeenCalled();
    const newRender = vi.fn();
    const stopNew = watchMarkerMetadata(player, newRender);
    oldCallback(0);
    queue.flush();
    expect(oldRender).not.toHaveBeenCalled();
    expect(newRender).toHaveBeenCalledTimes(1);
    stopNew();
  });

  it("does not render queued marker work after player disposal", () => {
    const player = createPlayer();
    vi.spyOn(player, "readyState").mockReturnValue(1);
    const queue = frames();
    const render = vi.fn();
    const stop = watchMarkerMetadata(player, render);
    player.dispose();
    queue.flush();
    expect(render).not.toHaveBeenCalled();
    expect(() => stop()).not.toThrow();
  });
});
