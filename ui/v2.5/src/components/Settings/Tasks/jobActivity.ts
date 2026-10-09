// Activity descriptions are sampled, deduplicated messages, never task counts.
export const MAX_ACTIVITY_MESSAGES = 500;

export function readActivityHistory(stored: string | null): string[] {
  try {
    const value: unknown = stored ? JSON.parse(stored) : [];
    return Array.isArray(value)
      ? value
          .filter((v): v is string => typeof v === "string" && !!v)
          .slice(-MAX_ACTIVITY_MESSAGES)
      : [];
  } catch {
    return [];
  }
}

export function appendActivityHistory(
  previous: string[],
  messages: readonly string[]
): string[] {
  const seen = new Set(previous);
  const added = messages.filter((message) => {
    if (!message || seen.has(message)) return false;
    seen.add(message);
    return true;
  });
  return added.length
    ? [...previous, ...added].slice(-MAX_ACTIVITY_MESSAGES)
    : previous;
}
