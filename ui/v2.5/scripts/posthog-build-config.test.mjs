import assert from "node:assert/strict";
import test from "node:test";
import {
  buildHeapMB,
  sourceMapReleaseVersion,
  uploadsEnabled,
} from "./posthog-build-config.mjs";

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

test("source maps use the embedded application SemVer, independent of revision", () => {
  for (const version of ["0.1.0", "1.2.3", "0.1.0-dev+sha.aaaaaaaaaaaa"]) {
    assert.equal(
      sourceMapReleaseVersion({
        VITE_APP_STASH_VERSION: version,
        VITE_APP_GITHASH: "b".repeat(40),
      }),
      version
    );
  }
  for (const version of [
    undefined,
    "",
    "v0.1.0",
    "candidate-feature-123-1",
    "a".repeat(40),
    "01.2.3",
    "0.1.0\n",
  ]) {
    assert.throws(
      () => sourceMapReleaseVersion({ VITE_APP_STASH_VERSION: version }),
      /application SemVer/
    );
  }
});
