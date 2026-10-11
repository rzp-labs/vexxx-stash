import { VideoJsPlayer } from "video.js";

// Keep deferred marker work scoped to the current scene/effect lifetime.
export function watchMarkerMetadata(
  player: VideoJsPlayer,
  renderMarkers: () => void
): () => void {
  let active = true;
  let frame: number | undefined;

  const schedule = () => {
    if (!active || player.isDisposed()) return;
    if (frame !== undefined) cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => {
      frame = undefined;
      if (active && !player.isDisposed()) renderMarkers();
    });
  };

  player.on("loadedmetadata", schedule);
  if (player.readyState() >= 1) schedule();

  return () => {
    active = false;
    if (frame !== undefined) cancelAnimationFrame(frame);
    player.off("loadedmetadata", schedule);
  };
}
