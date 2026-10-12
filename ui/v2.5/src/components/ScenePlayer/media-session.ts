import videojs, { VideoJsPlayer } from "video.js";
import posthogClient from "posthog-js/no-external";

// MediaSession belongs to the document, not to an individual video.js player.
const owners = new WeakMap<MediaSession, MediaSessionPlugin>();
const plugins = new WeakMap<MediaSession, MediaSessionPlugin[]>();

function reportError(error: unknown, operation: string): void {
  videojs.log.error(`Media session ${operation} failed`, error);
  try {
    posthogClient.captureException(error, {
      operation: `media_session.${operation}`,
      component: "MediaSessionPlugin",
    });
  } catch (captureError) {
    videojs.log.error("Could not report media session error", captureError);
  }
}

class MediaSessionPlugin extends videojs.getPlugin("plugin") {
  private session?: MediaSession;
  private disposed = false;
  private ready = false;
  private metadata?: [string, string, string];
  private playRequest = 0;
  private playbackFeedback?: ReturnType<VideoJsPlayer["createModal"]>;
  private registeredActions = new Set<MediaSessionAction>();

  constructor(player: VideoJsPlayer) {
    super(player);
    this.session = navigator.mediaSession;
    if (this.session) {
      const live = plugins.get(this.session) ?? [];
      live.push(this);
      plugins.set(this.session, live);
    }

    this.on("dispose", () => {
      this.disposed = true;
      this.playRequest += 1;
      this.metadata = undefined;
      this.clearFeedback();
      if (this.session) {
        const live = plugins
          .get(this.session)
          ?.filter((plugin) => plugin !== this);
        if (live?.length) plugins.set(this.session, live);
        else plugins.delete(this.session);
        this.updateOwner();
      }
    });

    player.ready(() => {
      if (this.ready || this.disposed || player.isDisposed() || !this.session)
        return;
      this.ready = true;
      this.updateOwner();
    });

    player.on("play", () => {
      this.clearFeedback();
      this.updatePlaybackState();
    });

    // Scene navigation reuses the player. Pending play and its feedback belong
    // to the old source, even when the next scene plays successfully.
    player.on("loadstart", () => {
      this.playRequest += 1;
      this.clearFeedback();
    });

    player.on("pause", () => {
      this.playRequest += 1;
      this.updatePlaybackState();
    });
    this.updatePlaybackState();
  }

  // manually set poster since it's only set on useEffect
  public setMetadata(title: string, artist: string, poster: string): void {
    if (this.disposed) return;
    this.metadata = [title, artist, poster];
    if (!this.isActive() || typeof MediaMetadata !== "function") return;
    try {
      this.session!.metadata = new MediaMetadata({
        title,
        artist,
        artwork: [
          {
            src: poster || this.player.poster() || "",
            type: "image/jpeg",
          },
        ],
      });
    } catch (error) {
      reportError(error, "metadata");
    }
  }

  private updatePlaybackState(): void {
    if (!this.isActive()) return;
    try {
      const playbackState = this.player.paused() ? "paused" : "playing";
      this.session!.playbackState = playbackState;
    } catch (error) {
      reportError(error, "playback_state");
    }
  }

  private isActive(): boolean {
    return (
      !this.disposed &&
      !!this.player &&
      !this.player.isDisposed() &&
      !!this.session &&
      owners.get(this.session) === this
    );
  }

  private updateOwner(): void {
    const { session } = this;
    if (!session) return;
    // Creation order chooses the newest usable player, regardless of ready order.
    // Pending replacements leave existing controls active; disposal allows the
    // newest surviving ready player to resume ownership.
    const next = plugins
      .get(session)
      ?.slice()
      .reverse()
      .find(
        (plugin) =>
          plugin.ready &&
          !plugin.disposed &&
          !!plugin.player &&
          !plugin.player.isDisposed()
      );
    const current = owners.get(session);
    if (next === current) return;
    if (!next) {
      current?.releaseActionHandlers();
      return;
    }
    next.registeredActions = new Set(current?.registeredActions);
    owners.set(session, next);
    next.player.addClass("vjs-media-session");
    next.setActionHandlers();
    next.updatePlaybackState();
    next.clearMetadata();
    if (next.metadata) next.setMetadata(...next.metadata);
  }

  private clearMetadata(): void {
    if (!this.session) return;
    try {
      this.session.metadata = null;
    } catch (error) {
      reportError(error, "metadata");
    }
  }

  private async play(): Promise<void> {
    const request = ++this.playRequest;
    try {
      await this.player.play();
    } catch (error) {
      const expected =
        error instanceof DOMException &&
        (error.name === "NotAllowedError" || error.name === "AbortError");
      if (!expected) reportError(error, "play");
      // A pause, replacement player or disposal may interrupt a pending play.
      if (
        !this.isActive() ||
        request !== this.playRequest ||
        !this.player.paused()
      )
        return;
      if (expected) videojs.log.warn("Media session play was declined", error);
      try {
        this.dismissFeedback();
        this.playbackFeedback = this.player.createModal(
          "Playback could not be started from system media controls. Close this message and use the player's Play button to try again.",
          { temporary: true, pauseOnOpen: false }
        );
      } catch (modalError) {
        reportError(modalError, "play_feedback");
      }
      return;
    }
    if (this.isActive() && request === this.playRequest) {
      try {
        this.dismissFeedback();
      } catch (error) {
        reportError(error, "play_feedback");
      }
    }
  }

  private dismissFeedback(): void {
    const { playbackFeedback: feedback } = this;
    this.playbackFeedback = undefined;
    if (feedback && !feedback.isDisposed()) feedback.close();
  }

  private clearFeedback(): void {
    try {
      this.dismissFeedback();
    } catch (error) {
      reportError(error, "play_feedback");
    }
  }

  private setActionHandlers(): void {
    if (
      !this.isActive() ||
      typeof this.session?.setActionHandler !== "function"
    )
      return;
    const handlers: [MediaSessionAction, () => void][] = [
      [
        "play",
        () => {
          void this.play();
        },
      ],
      [
        "pause",
        () => {
          this.player.pause();
        },
      ],
      [
        "nexttrack",
        () => {
          this.player.skipButtons()?.handleForward();
        },
      ],
      [
        "previoustrack",
        () => {
          this.player.skipButtons()?.handleBackward();
        },
      ],
    ];
    handlers.forEach(([action, handler]) => {
      try {
        this.session!.setActionHandler(action, () => {
          if (!this.isActive()) return;
          try {
            handler();
          } catch (error) {
            reportError(error, action);
          }
        });
        this.registeredActions.add(action);
      } catch (error) {
        // One unsupported action must not prevent the other controls working.
        if (
          !(error instanceof DOMException && error.name === "NotSupportedError")
        )
          reportError(error, `register.${action}`);
      }
    });
  }

  private releaseActionHandlers(): void {
    const { session } = this;
    if (!session || owners.get(session) !== this) return;
    owners.delete(session);
    this.registeredActions.forEach((action) => {
      try {
        session.setActionHandler(action, null);
      } catch (error) {
        reportError(error, `release.${action}`);
      }
    });
    this.registeredActions.clear();
    this.clearMetadata();
    try {
      session.playbackState = "none";
    } catch (error) {
      reportError(error, "playback_state");
    }
  }
}

videojs.registerPlugin("mediaSession", MediaSessionPlugin);

/* eslint-disable @typescript-eslint/naming-convention */
declare module "video.js" {
  interface VideoJsPlayer {
    mediaSession: () => MediaSessionPlugin;
  }
}

export default MediaSessionPlugin;
