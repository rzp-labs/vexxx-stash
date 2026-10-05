import assert from "node:assert/strict";
import test from "node:test";
import { buildHeapMB, uploadsEnabled } from "./posthog-build-config.mjs";

test("validation retains 4GiB even when upload credentials exist", () => {
  const env = {
    POSTHOG_UPLOAD_REQUIRED: "false",
    POSTHOG_API_KEY: "dummy",
    POSTHOG_PROJECT_ID: "dummy",
  };
  assert.equal(uploadsEnabled("build", env), false);
  assert.equal(buildHeapMB(env), 4096);
});
test("missing upload settings never raise the memory allowance", () => {
  for (const env of [
    {},
    { POSTHOG_UPLOAD_REQUIRED: "true" },
    { POSTHOG_API_KEY: "dummy" },
  ]) {
    assert.equal(uploadsEnabled("build", env), false);
    assert.equal(buildHeapMB(env), 4096);
  }
});
test("only enabled build uploads get the tested source-map allowance", () => {
  const env = {
    POSTHOG_UPLOAD_REQUIRED: "true",
    POSTHOG_API_KEY: "dummy",
    POSTHOG_PROJECT_ID: "dummy",
  };
  assert.equal(uploadsEnabled("build", env), true);
  assert.equal(uploadsEnabled("serve", env), false);
  assert.equal(buildHeapMB(env), 6144);
});
test("explicit false also disables optional uploads", () => {
  const env = { POSTHOG_API_KEY: "dummy", POSTHOG_PROJECT_ID: "dummy" };
  assert.equal(uploadsEnabled("build", env), true);
  assert.equal(buildHeapMB(env), 6144);
  assert.equal(
    uploadsEnabled("build", { ...env, POSTHOG_UPLOAD_REQUIRED: "false" }),
    false
  );
});
