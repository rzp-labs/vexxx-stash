import { describe, expect, it } from "vitest";
import { appendActivityHistory, readActivityHistory } from "./jobActivity";

describe("sampled activity history", () => {
  it("retains the newest 500 messages beyond 1200 updates without task counts", () => {
    let history: string[] = [];
    for (let n = 1; n <= 1200; n++) {
      history = appendActivityHistory(history, [
        `activity ${n}`,
        `activity ${n}`,
      ]);
    }
    expect(history).toHaveLength(500);
    expect(history[0]).toBe("activity 701");
    expect(history[499]).toBe("activity 1200");
    expect(appendActivityHistory(history, ["activity 1200", ""])).toBe(history);
    expect(readActivityHistory(JSON.stringify(history))).toEqual(history);
  });
  it("bounds and validates persisted history on remount", () => {
    const old = Array.from({ length: 1200 }, (_, i) => `old ${i}`);
    expect(readActivityHistory(JSON.stringify(old))).toEqual(old.slice(-500));
    for (const stored of [null, "bad JSON", "{}", "123", '"text"']) {
      expect(readActivityHistory(stored)).toEqual([]);
    }
    expect(readActivityHistory('[1,null,"",{},"activity"]')).toEqual([
      "activity",
    ]);
  });
});
