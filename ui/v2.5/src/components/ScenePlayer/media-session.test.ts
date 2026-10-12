import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import videojs, { VideoJsPlayer } from "video.js";
import posthogClient from "posthog-js/no-external";
import MediaSessionPlugin from "./media-session";

vi.mock("posthog-js/no-external", () => ({
  default: { captureException: vi.fn() },
}));
vi.mock("video.js", () => ({
  default: {
    getPlugin: () =>
      class {
        private onDispose = () => {};
        constructor(public player: VideoJsPlayer) {
          player.on("dispose", () => this.dispose());
        }
        on(event: string, callback: () => void) {
          if (event === "dispose") this.onDispose = callback;
        }
        dispose() {
          this.onDispose();
          this.player = null as unknown as VideoJsPlayer;
        }
      },
    registerPlugin: vi.fn(),
    log: { error: vi.fn(), warn: vi.fn() },
  },
}));

function createPlayer() {
  const events = new Map<string, () => void>();
  let ready = () => {};
  let disposed = false;
  const forward = vi.fn();
  const backward = vi.fn();
  const mock = {
    ready: vi.fn((callback: () => void) => {
      ready = callback;
    }),
    on: vi.fn((event: string, callback: () => void) => {
      events.set(event, callback);
    }),
    one: vi.fn((event: string, callback: () => void) => {
      events.set(event, callback);
    }),
    addClass: vi.fn(),
    isDisposed: () => disposed,
    paused: vi.fn(() => true),
    poster: vi.fn(() => ""),
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(() => {
      events.get("pause")?.();
    }),
    createModal: vi.fn(),
    skipButtons: vi.fn(() => ({
      handleForward: forward,
      handleBackward: backward,
    })),
  };
  const plugin = new MediaSessionPlugin(mock as unknown as VideoJsPlayer);
  return {
    mock,
    plugin,
    forward,
    backward,
    ready: () => ready(),
    emit: (event: string) => events.get(event)?.(),
    dispose: () => {
      disposed = true;
      events.get("dispose")?.();
    },
  };
}

function session() {
  const handlers = new Map<MediaSessionAction, (() => void) | null>();
  const api = {
    playbackState: "none",
    metadata: null,
    setActionHandler: vi.fn(
      (action: MediaSessionAction, callback: (() => void) | null) => {
        handlers.set(action, callback);
      }
    ),
  };
  vi.stubGlobal("navigator", { mediaSession: api });
  return { api, handlers };
}

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("optional media session controls", () => {
  it.each([{}, { mediaSession: undefined }, { mediaSession: null }])(
    "leaves normal playback usable without a media session: %j",
    (navigatorValue) => {
      vi.stubGlobal("navigator", navigatorValue);
      const p = createPlayer();
      expect(() => p.ready()).not.toThrow();
      expect(() =>
        p.plugin.setMetadata("title", "artist", "poster")
      ).not.toThrow();
      expect(() => {
        p.emit("play");
        p.emit("pause");
        p.dispose();
      }).not.toThrow();
      expect(posthogClient.captureException).not.toHaveBeenCalled();
      expect(p.mock.createModal).not.toHaveBeenCalled();
    }
  );

  it("supports playback state without action handlers or MediaMetadata", () => {
    const api = { playbackState: "none" };
    vi.stubGlobal("navigator", { mediaSession: api });
    vi.stubGlobal("MediaMetadata", undefined);
    const p = createPlayer();
    p.ready();
    p.plugin.setMetadata("title", "artist", "poster");
    p.mock.paused.mockReturnValue(false);
    p.emit("play");
    expect(api.playbackState).toBe("playing");
    p.dispose();
    expect(posthogClient.captureException).not.toHaveBeenCalled();
  });

  it("registers working actions and updates metadata/state, then releases them", async () => {
    const { api, handlers } = session();
    vi.stubGlobal(
      "MediaMetadata",
      class {
        constructor(public data: unknown) {}
      }
    );
    const p = createPlayer();
    p.ready();
    p.plugin.setMetadata("title", "artist", "poster");
    expect(api.metadata).toMatchObject({
      data: {
        title: "title",
        artist: "artist",
        artwork: [{ src: "poster", type: "image/jpeg" }],
      },
    });
    handlers.get("play")?.();
    await Promise.resolve();
    handlers.get("pause")?.();
    handlers.get("nexttrack")?.();
    handlers.get("previoustrack")?.();
    expect(p.mock.play).toHaveBeenCalledOnce();
    expect(p.mock.pause).toHaveBeenCalledOnce();
    expect(p.forward).toHaveBeenCalledOnce();
    expect(p.backward).toHaveBeenCalledOnce();
    const stalePlay = handlers.get("play");
    p.dispose();
    expect([...handlers.values()]).toEqual([null, null, null, null]);
    stalePlay?.();
    expect(p.mock.play).toHaveBeenCalledOnce();
    expect(posthogClient.captureException).not.toHaveBeenCalled();
  });

  it("continues registering after a rejected action and reports unexpected failures", () => {
    const { api, handlers } = session();
    api.setActionHandler.mockImplementationOnce(() => {
      throw new DOMException("unsupported", "NotSupportedError");
    });
    api.setActionHandler.mockImplementationOnce(() => {
      throw new Error("registration failed");
    });
    const p = createPlayer();
    p.ready();
    expect(handlers.has("nexttrack")).toBe(true);
    expect(handlers.has("previoustrack")).toBe(true);
    expect(posthogClient.captureException).toHaveBeenCalledExactlyOnceWith(
      expect.any(Error),
      expect.objectContaining({ operation: "media_session.register.pause" })
    );
  });

  it.each([
    new DOMException("denied", "NotAllowedError"),
    new Error("decoder failed"),
  ])(
    "handles rejected play with retry guidance and allows another attempt: %s",
    async (failure) => {
      const { handlers } = session();
      const p = createPlayer();
      p.mock.play.mockRejectedValueOnce(failure);
      p.ready();
      handlers.get("play")?.();
      await vi.waitFor(() =>
        expect(p.mock.createModal).toHaveBeenCalledWith(
          expect.stringContaining("Play button"),
          { temporary: true, pauseOnOpen: false }
        )
      );
      expect(posthogClient.captureException).toHaveBeenCalledTimes(
        failure.name === "NotAllowedError" ? 0 : 1
      );
      handlers.get("play")?.();
      await Promise.resolve();
      expect(p.mock.play).toHaveBeenCalledTimes(2);
    }
  );

  it.each(["pause", "dispose", "replace", "loadstart"])(
    "does not show stale play failure after %s",
    async (interruption) => {
      const { handlers } = session();
      const p = createPlayer();
      let reject!: (error: unknown) => void;
      p.mock.play.mockImplementationOnce(
        () =>
          new Promise((_, rejectPromise) => {
            reject = rejectPromise;
          })
      );
      p.ready();
      handlers.get("play")?.();
      if (interruption === "pause") p.emit("pause");
      if (interruption === "dispose") p.dispose();
      if (interruption === "replace") createPlayer().ready();
      if (interruption === "loadstart") p.emit("loadstart");
      reject(new DOMException("interrupted", "AbortError"));
      await Promise.resolve();
      await Promise.resolve();
      expect(p.mock.createModal).not.toHaveBeenCalled();
      expect(posthogClient.captureException).not.toHaveBeenCalled();
    }
  );

  it("replaces repeated failure feedback and dismisses it after a successful retry", async () => {
    const { handlers } = session();
    const p = createPlayer();
    const firstModal = { close: vi.fn(), isDisposed: () => false };
    const secondModal = { close: vi.fn(), isDisposed: () => false };
    p.mock.createModal
      .mockReturnValueOnce(firstModal)
      .mockReturnValueOnce(secondModal);
    p.mock.play.mockRejectedValueOnce(
      new DOMException("denied", "NotAllowedError")
    );
    p.mock.play.mockRejectedValueOnce(
      new DOMException("denied again", "NotAllowedError")
    );
    p.ready();
    handlers.get("play")?.();
    await vi.waitFor(() => expect(p.mock.createModal).toHaveBeenCalledOnce());
    handlers.get("play")?.();
    await vi.waitFor(() => expect(p.mock.createModal).toHaveBeenCalledTimes(2));
    expect(firstModal.close).toHaveBeenCalledOnce();
    handlers.get("play")?.();
    await vi.waitFor(() => expect(secondModal.close).toHaveBeenCalledOnce());
    expect(p.mock.play).toHaveBeenCalledTimes(3);
  });

  it("keeps replacement handlers when the old player is disposed, and guards late ready", () => {
    const { api, handlers } = session();
    const old = createPlayer();
    old.ready();
    const replacement = createPlayer();
    replacement.ready();
    api.setActionHandler.mockClear();
    old.dispose();
    old.ready();
    old.emit("play");
    expect(api.setActionHandler).not.toHaveBeenCalled();
    handlers.get("pause")?.();
    expect(replacement.mock.pause).toHaveBeenCalledOnce();
    replacement.dispose();
    expect([...handlers.values()]).toEqual([null, null, null, null]);
  });

  it("keeps the current owner's controls while a replacement waits for ready", () => {
    const { api, handlers } = session();
    const current = createPlayer();
    current.ready();
    current.mock.paused.mockReturnValue(false);
    current.emit("play");
    const replacement = createPlayer();
    expect(api.playbackState).toBe("playing");
    handlers.get("pause")?.();
    expect(current.mock.pause).toHaveBeenCalledOnce();
    expect(replacement.mock.pause).not.toHaveBeenCalled();
    replacement.ready();
    handlers.get("pause")?.();
    expect(replacement.mock.pause).toHaveBeenCalledOnce();
    expect(current.mock.pause).toHaveBeenCalledOnce();
  });

  it("preserves the current handlers when a pending replacement is disposed", () => {
    const { handlers } = session();
    const current = createPlayer();
    current.ready();
    const replacement = createPlayer();
    replacement.dispose();
    replacement.ready();
    handlers.get("pause")?.();
    expect(current.mock.pause).toHaveBeenCalledOnce();
    expect(replacement.mock.pause).not.toHaveBeenCalled();
    current.dispose();
    expect([...handlers.values()]).toEqual([null, null, null, null]);
  });

  it("publishes metadata supplied before ready when ownership transfers", () => {
    const { api } = session();
    vi.stubGlobal(
      "MediaMetadata",
      class {
        constructor(public data: unknown) {}
      }
    );
    const current = createPlayer();
    current.ready();
    current.plugin.setMetadata("current", "artist", "poster");
    const replacement = createPlayer();
    replacement.plugin.setMetadata("replacement", "artist", "poster");
    expect(api.metadata).toMatchObject({ data: { title: "current" } });
    replacement.ready();
    expect(api.metadata).toMatchObject({ data: { title: "replacement" } });
  });

  it.each(["loadstart", "play", "plugin dispose", "player dispose"])(
    "closes already-open failure feedback on %s",
    async (transition) => {
      const { handlers } = session();
      const p = createPlayer();
      const modal = { close: vi.fn(), isDisposed: () => false };
      p.mock.createModal.mockReturnValueOnce(modal);
      p.mock.play.mockRejectedValueOnce(
        new DOMException("denied", "NotAllowedError")
      );
      p.ready();
      handlers.get("play")?.();
      await vi.waitFor(() => expect(p.mock.createModal).toHaveBeenCalledOnce());
      if (transition === "plugin dispose") p.plugin.dispose();
      else if (transition === "player dispose") p.dispose();
      else p.emit(transition);
      expect(modal.close).toHaveBeenCalledOnce();
      expect(posthogClient.captureException).not.toHaveBeenCalled();
    }
  );

  it("keeps unexpected old-source rejection diagnostic while suppressing stale feedback", async () => {
    const { handlers } = session();
    const p = createPlayer();
    let reject!: (error: unknown) => void;
    p.mock.play.mockImplementationOnce(
      () =>
        new Promise((_, rejectPromise) => {
          reject = rejectPromise;
        })
    );
    p.ready();
    handlers.get("play")?.();
    p.emit("loadstart");
    reject(new Error("old decoder failed"));
    await vi.waitFor(() =>
      expect(posthogClient.captureException).toHaveBeenCalledOnce()
    );
    expect(p.mock.createModal).not.toHaveBeenCalled();
  });

  it.each([
    new DOMException("interrupted", "AbortError"),
    new Error("late decoder failure"),
  ])(
    "does not reopen obsolete feedback after ordinary playback succeeds: %s",
    async (failure) => {
      const { handlers } = session();
      const p = createPlayer();
      let reject!: (error: unknown) => void;
      p.mock.play.mockImplementationOnce(
        () =>
          new Promise((_, rejectPromise) => {
            reject = rejectPromise;
          })
      );
      p.ready();
      handlers.get("play")?.();
      p.mock.paused.mockReturnValue(false);
      p.emit("play");
      reject(failure);
      await Promise.resolve();
      await Promise.resolve();
      expect(p.mock.createModal).not.toHaveBeenCalled();
      expect(posthogClient.captureException).toHaveBeenCalledTimes(
        failure instanceof DOMException ? 0 : 1
      );
    }
  );

  it("releases handlers on explicit plugin disposal while leaving the player usable", () => {
    const { handlers } = session();
    const p = createPlayer();
    p.ready();
    p.plugin.dispose();
    expect([...handlers.values()]).toEqual([null, null, null, null]);
    p.mock.play();
    expect(p.mock.play).toHaveBeenCalledOnce();
  });

  it("retains diagnostics if metadata, state, callbacks or reporting fail", async () => {
    const { api, handlers } = session();
    vi.stubGlobal(
      "MediaMetadata",
      class {
        constructor() {
          throw new Error("metadata failed");
        }
      }
    );
    const p = createPlayer();
    p.ready();
    p.plugin.setMetadata("title", "artist", "poster");
    Object.defineProperty(api, "playbackState", {
      set: () => {
        throw new Error("state failed");
      },
    });
    p.emit("play");
    p.mock.pause.mockImplementationOnce(() => {
      throw new Error("pause failed");
    });
    handlers.get("pause")?.();
    expect(posthogClient.captureException).toHaveBeenCalledTimes(3);
    vi.mocked(posthogClient.captureException).mockImplementationOnce(() => {
      throw new Error("report failed");
    });
    p.mock.play.mockRejectedValueOnce(new Error("play failed"));
    p.mock.createModal.mockImplementationOnce(() => {
      throw new Error("modal failed");
    });
    handlers.get("play")?.();
    await vi.waitFor(() =>
      expect(videojs.log.error).toHaveBeenCalledWith(
        "Could not report media session error",
        expect.any(Error)
      )
    );
    expect(videojs.log.error).toHaveBeenCalledWith(
      "Media session play_feedback failed",
      expect.any(Error)
    );
  });
});
