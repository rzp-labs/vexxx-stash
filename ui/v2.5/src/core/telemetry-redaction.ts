// Preserve diagnostic prose and executable property names. Redact data-bearing
// slots and credential/path patterns rather than maintaining an error vocabulary.
const maxMessageLength = 2048;
const privateField =
  /(?:^|_)(?:token|secret|password|passwd|authorization|cookie|api_key|private_?keys?|credential|username|email|title|filename|filepath|path|url|uri|body|payload|headers|input|content|variables|args|attachment|data)(?:_|$)|^(?:response|request)$/i;

export function diagnosticMessage(value: unknown): string {
  if (typeof value !== "string") return "Non-string error message [redacted]";
  // Redact before truncation, so a cut-off token is never emitted as a prefix.
  // Decoding also exposes percent-encoded credentials/paths to the same rules.
  let message = value;
  for (let pass = 0; pass < 2; pass += 1) {
    try {
      const decoded = decodeURIComponent(message);
      if (decoded === message) break;
      message = decoded;
    } catch {
      message = message.replace(/(?:%[0-9a-f]{2})+/gi, (encoded) => {
        try {
          return decodeURIComponent(encoded);
        } catch {
          return encoded;
        }
      });
    }
  }
  // Native JSON parser previews include arbitrary input, including personal data.
  if (/^Unexpected (?:token|non-whitespace character).*JSON/s.test(message)) {
    const position = message.match(
      /^Unexpected non-whitespace character after JSON at position (\d{1,8})(?: \(line (\d{1,8}) column (\d{1,8})\))?$/
    );
    return `Invalid JSON${
      position
        ? ` at position ${position[1]}${
            position[3] ? ` column ${position[3]}` : ""
          }`
        : ""
    } [input redacted]`;
  }
  // These source call sites concatenate raw server response text. Keep the
  // operation/status but never treat the appended body as diagnostic prose.
  message = message
    // PEM can span many lines and exceed the output limit. Remove the entire
    // block (or incomplete block tail) before truncation or other substitutions.
    .replace(
      /-----BEGIN ((?:[A-Z0-9]+ )*PRIVATE KEY)-----[\s\S]*?(?:-----END \1-----|$)/g,
      "[private key redacted]"
    )
    .replace(
      /^(HTTP|Backend error|funscript fetch failed|Script upload failed \(HTTP|Auth failed \(HTTP)[ :]+([1-5]\d{2})(?:\))?(?:(?::|\s).*)?$/s,
      (_, operation: string, status: string) =>
        `${operation} ${status}${
          operation.includes("(") ? ")" : ""
        } [response redacted]`
    )
    .replace(
      /^(Failed to fetch (?:sprite|VTT|image)|Status check failed): (.*)$/s,
      (_, operation: string, response: string) =>
        `${operation}: ${
          /^[1-5]\d{2}$/.test(response) ? response : "[response redacted]"
        }`
    )
    .replace(
      /\b(?:response(?: body)?|request body|payload|input|content)\s*[:=]\s*[\s\S]*/gi,
      "[content redacted]"
    )
    .replace(/\{\s*"[^"\n]+"\s*:[\s\S]*/g, "[content redacted]")
    .replace(/<(!doctype|html|body)[\s\S]*/gi, "[content redacted]")
    .replace(
      /\b(?:https?|wss?|ftp|file|data|blob):[^\s<>"']+/gi,
      "[URL redacted]"
    )
    // A Cookie header contains multiple semicolon-delimited values. Its whole
    // line is private, including cookies whose names do not look like secrets.
    .replace(
      /(\b(?:cookie|set-cookie)[ \t]*:[ \t]*)[^\r\n]*/gi,
      "$1[credential redacted]"
    )
    .replace(
      /\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/gi,
      "[credential redacted]"
    )
    // Without quotes there is no reliable word/delimiter boundary for a
    // password. Redact the complete assignment tail through the end of its line.
    .replace(
      /(["']?(?:[\w.-]*(?:token|secret|password|passwd|api[_-]?key|authorization|cookie|credential)[\w.-]*)["']?[ \t]*[:=][ \t]*)(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\r\n]+)/gi,
      "$1[credential redacted]"
    )
    .replace(
      /(["']?(?:username|email|title|filename|filepath|path|url)["']?\s*[:=]\s*)(?:\[[^\]\n]*\]|"[^"\n]*"|'[^'\n]*'|[^\s,;}&]+)/gi,
      "$1[private content redacted]"
    )
    .replace(
      /\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?\b/g,
      "[credential redacted]"
    )
    .replace(
      /\b(?:sk[-_](?:live[-_]|test[-_])?|gh[pousr]_|github_pat_|phx_)[A-Za-z0-9_-]{8,}\b/g,
      "[credential redacted]"
    )
    .replace(/(["'])(?:[A-Z]:[\\/]|\\\\|\/|~\/)[^"'\n]*\1/gi, "[path redacted]")
    .replace(
      /(["'])[^"'\n]+\.(?:mp4|mkv|mov|avi|webm|mp3|wav|flac|jpg|jpeg|png|gif|webp|funscript)\1/gi,
      "[media redacted]"
    )
    .replace(
      /\b[A-Z]:[\\/][^\s<>"']+|\\\\[^\s<>"']+|(?:\/|~\/)[^\s<>"'()\/]+(?:\/[^\s<>"'),;]+)+/gi,
      "[path redacted]"
    )
    .replace(
      /[^\s<>"'()]+\.(?:mp4|mkv|mov|avi|webm|mp3|wav|flac|jpg|jpeg|png|gif|webp|funscript)(?:\?[^\s<>"']*)?\b/gi,
      "[media redacted]"
    )
    .replace(/\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi, "[email redacted]");
  return message.length > maxMessageLength
    ? `${message.slice(0, maxMessageLength)} [truncated]`
    : message;
}

// Context is deliberately opt-in at the event envelope. Within that diagnostic
// container, retain novel technical keys and typed values, not raw bodies/input.
// Bounds also stop circular causes or oversized caller objects from breaking capture.
export function diagnosticContext(value: unknown): unknown {
  return sanitizeContext(value, 0, { remaining: 200 });
}

function sanitizeContext(
  value: unknown,
  depth: number,
  budget: { remaining: number }
): unknown {
  budget.remaining -= 1;
  if (budget.remaining < 0) return "[context truncated]";
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "number")
    return Number.isFinite(value) ? value : undefined;
  if (typeof value === "string") return diagnosticMessage(value);
  if (depth >= 4) return "[context truncated]";
  if (Array.isArray(value))
    return value
      .slice(0, 20)
      .map((item) => sanitizeContext(item, depth + 1, budget) ?? null);
  if (
    typeof value !== "object" ||
    Object.getPrototypeOf(value) !== Object.prototype
  )
    return undefined;
  return Object.fromEntries(
    Object.entries(value)
      .slice(0, 30)
      .flatMap(([key, item]) => {
        if (!/^[A-Za-z_$][\w.$-]{0,79}$/.test(key)) return [];
        if (
          privateField.test(
            key.replace(/([a-z])([A-Z])/g, "$1_$2").replace(/[.-]/g, "_")
          )
        )
          return [[key, "[private content redacted]"]];
        const safe = sanitizeContext(item, depth + 1, budget);
        return safe === undefined ? [] : [[key, safe]];
      })
  );
}
