import videojs, { VideoJsPlayer } from "video.js";

declare module "video.js" {
  // eslint-disable-next-line @typescript-eslint/naming-convention
  interface VideoJsPlayer {
    virtualStart?: number;
    virtualEnd?: number;
  }
}

export function timelinePercent(rect: ClientRect, clientX: number) {
  return Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
}

export function timelineTime(player: VideoJsPlayer, percent: number) {
  const start = player.virtualStart ?? 0;
  const end = player.virtualEnd ?? player.duration();
  return start + percent * (end - start);
}

// Video.js 7 exposes updateTime at runtime, but its typings omit it.
interface ITimeTooltip extends videojs.TimeToolTip {
  updateTime(
    rect: ClientRect,
    percent: number,
    time: number,
    callback: () => void
  ): void;
}

class TimelineMouseTimeDisplay extends videojs.getComponent(
  "MouseTimeDisplay"
) {
  update(rect: ClientRect, percent: number) {
    const player = this.player();
    if (player.virtualStart === undefined && player.virtualEnd === undefined) {
      super.update(rect, percent);
      return;
    }
    const tooltip = this.getChild("timeTooltip") as ITimeTooltip;
    tooltip.updateTime(rect, percent, timelineTime(player, percent), () => {
      (this.el() as HTMLElement).style.left = `${rect.width * percent}px`;
    });
  }
}

videojs.registerComponent("MouseTimeDisplay", TimelineMouseTimeDisplay);

// Position displays must follow seeks even when playback remains paused.
export function watchPlayerPosition(
  player: VideoJsPlayer,
  setTime: (time: number) => void
) {
  function update() {
    const time = player.currentTime();
    setTime(time);
    if (player.virtualStart !== undefined || player.virtualEnd !== undefined) {
      const start = player.virtualStart ?? 0;
      const end = player.virtualEnd ?? player.duration();
      if (end > start) {
        const percent = Math.min(
          1,
          Math.max(0, (time - start) / (end - start))
        );
        (player.el() as HTMLElement).style.setProperty(
          "--vjs-virtual-progress",
          `${(percent * 100).toFixed(2)}%`
        );
      }
    }
  }

  player.on("timeupdate", update);
  player.on("seeked", update);
  return () => {
    player.off("timeupdate", update);
    player.off("seeked", update);
  };
}
