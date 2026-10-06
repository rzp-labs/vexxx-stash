// Keep upload eligibility and its memory allowance identical in the launcher
// and Vite. Explicit false always wins, including when credentials are present.
export function uploadsEnabled(command, env) {
  return (
    command === "build" &&
    env.POSTHOG_UPLOAD_REQUIRED !== "false" &&
    Boolean(env.POSTHOG_API_KEY && env.POSTHOG_PROJECT_ID)
  );
}

export function buildHeapMB(env) {
  return uploadsEnabled("build", env) ? 6144 : 4096;
}

// Uploads must use the same application version as the embedded UI metadata.
export function sourceMapReleaseVersion(env) {
  const version = env.VITE_APP_STASH_VERSION;
  const number = "(?:0|[1-9][0-9]*)";
  const semver = new RegExp(
    `^${number}\\.${number}\\.${number}(?:-dev\\+sha\\.[a-f0-9]{12})?$`
  );
  if (typeof version !== "string" || version.match(semver)?.[0] !== version) {
    throw new Error(
      "PostHog source-map upload requires the application SemVer"
    );
  }
  return version;
}
