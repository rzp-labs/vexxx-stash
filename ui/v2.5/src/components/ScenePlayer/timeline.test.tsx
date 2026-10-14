import { useEffect, useState } from "react";
import videojs, { VideoJsPlayer } from "video.js";
import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type * as GQL from "src/core/generated-graphql";
import TextUtils from "src/utils/text";
import { ScenePlayerScrubber } from "./ScenePlayerScrubber";
import { timelinePercent, timelineTime, watchPlayerPosition } from "./timeline";

const sprites = vi.hoisted(() => [
  { start: 1, end: 2, x: 0, y: 0, w: 100, h: 50, url: "/sprite.jpg" },
  { start: 2, end: 3, x: 100, y: 0, w: 100, h: 50, url: "/sprite.jpg" },
]);
vi.mock("src/hooks/sprite", () => ({
  useSpriteInfo: () => sprites,
}));

let player: VideoJsPlayer;
let time: number;
const resizeObserver = window.ResizeObserver;

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  const video = document.createElement("video");
  document.body.appendChild(video);
  player = videojs(video, { controls: true });
  time = 3;
  vi.spyOn(player, "currentTime").mockImplementation(() => time);
  vi.spyOn(player, "duration").mockReturnValue(10);
  vi.spyOn(player, "paused").mockReturnValue(true);
  player.virtualStart = 1;
  player.virtualEnd = 3;
});

afterEach(() => {
  player.dispose();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.ResizeObserver = resizeObserver;
});

describe("player position synchronization", () => {
  it("updates footer, virtual fill and real sprite strip after paused backward and forward seeks", () => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    window.ResizeObserver = class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(element: Element) {
        this.callback([{ target: element } as ResizeObserverEntry], this);
      }
      unobserve() {}
      disconnect() {}
    };
    const file = { duration: 10 } as GQL.VideoFileDataFragment;
    const scene = {
      paths: { vtt: "/sprites.vtt" },
      scene_markers: [],
    } as unknown as GQL.SceneDataFragment;
    const onSeek = vi.fn();
    const onScroll = vi.fn();
    function Displays() {
      const [position, setPosition] = useState(3);
      useEffect(() => watchPlayerPosition(player, setPosition), []);
      return (
        <>
          <output>
            {TextUtils.secondsToTimestamp(Math.max(0, position - 1))} / 0:02
          </output>
          <ScenePlayerScrubber
            file={file}
            scene={scene}
            time={position}
            start={1}
            end={3}
            onSeek={onSeek}
            onScroll={onScroll}
          />
        </>
      );
    }
    const view = render(<Displays />);
    const slider = view.container.querySelector("[data-sprite-item-id]")!
      .parentElement!;
    function seek(target: number) {
      act(() => {
        time = target;
        player.trigger("seeked");
      });
    }
    for (const [target, footer, fill, sliderPosition] of [
      [1, "0:00 / 0:02", "0.00%", "translateX(200px)"],
      [2, "0:01 / 0:02", "50.00%", "translateX(100px)"],
      [3, "0:02 / 0:02", "100.00%", "translateX(0px)"],
    ] as const) {
      seek(target);
      expect(view.container.querySelector("output")).toHaveTextContent(footer);
      expect(
        (player.el() as HTMLElement).style.getPropertyValue(
          "--vjs-virtual-progress"
        )
      ).toBe(fill);
      expect(slider.style.transform).toBe(sliderPosition);
      expect(player.paused()).toBe(true);
    }
    expect(onSeek).not.toHaveBeenCalled();
  });

  it("keeps playing timeupdates and paused timeupdates synchronized", () => {
    const display = vi.fn();
    const stop = watchPlayerPosition(player, display);
    for (const paused of [false, true]) {
      vi.mocked(player.paused).mockReturnValue(paused);
      time = 2;
      player.trigger("timeupdate");
      expect(display).toHaveBeenLastCalledWith(2);
      expect(
        (player.el() as HTMLElement).style.getPropertyValue(
          "--vjs-virtual-progress"
        )
      ).toBe("50.00%");
    }
    stop();
    display.mockClear();
    player.trigger("timeupdate");
    player.trigger("seeked");
    expect(display).not.toHaveBeenCalled();
  });

  it("preserves full-file displays and clamps virtual fill", () => {
    const display = vi.fn();
    const stop = watchPlayerPosition(player, display);
    time = 0;
    player.trigger("seeked");
    expect(
      (player.el() as HTMLElement).style.getPropertyValue(
        "--vjs-virtual-progress"
      )
    ).toBe("0.00%");
    time = 10;
    player.trigger("seeked");
    expect(
      (player.el() as HTMLElement).style.getPropertyValue(
        "--vjs-virtual-progress"
      )
    ).toBe("100.00%");
    delete player.virtualStart;
    delete player.virtualEnd;
    (player.el() as HTMLElement).style.removeProperty("--vjs-virtual-progress");
    time = 5;
    player.trigger("timeupdate");
    expect(display).toHaveBeenLastCalledWith(5);
    expect(
      (player.el() as HTMLElement).style.getPropertyValue(
        "--vjs-virtual-progress"
      )
    ).toBe("");
    stop();
  });
});

describe("shared hover and seek mapping", () => {
  it.each([
    [-100, 1],
    [100, 1],
    [500, 2],
    [897.6, 2.994],
    [900, 3],
    [1000, 3],
  ])(
    "maps pointer x=%s to target %s within virtual bounds",
    (clientX, expected) => {
      const rect = { left: 100, width: 800 } as ClientRect;
      expect(timelineTime(player, timelinePercent(rect, clientX))).toBeCloseTo(
        expected,
        8
      );
    }
  );
});
