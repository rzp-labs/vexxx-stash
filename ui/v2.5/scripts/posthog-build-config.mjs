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
