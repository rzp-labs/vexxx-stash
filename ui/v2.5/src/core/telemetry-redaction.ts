// Preserve diagnostic prose and executable property names. Redact data-bearing
// slots and credential/path patterns rather than maintaining an error vocabulary.
export const diagnosticLimits = {
  message: 8192,
  inspectedCharacters: 65536,
  depth: 8,
  nodes: 2000,
  contextCharacters: 131072,
  objectFields: 100,
  arrayItems: 100,
  exceptions: 50,
  frames: 50,
  inspectedEntries: 10000,
  inspectedProperties: 100000,
} as const;

export interface IDiagnosticAccounting {
  remaining: number;
  redacted: number;
  truncated: number;
  omitted: number;
  cycles: number;
  accessors: number;
  inspected: number;
  charactersRemaining: number;
}
export const diagnosticAccounting = (): IDiagnosticAccounting => ({
  remaining: diagnosticLimits.nodes,
  redacted: 0,
  truncated: 0,
  omitted: 0,
  cycles: 0,
  accessors: 0,
  inspected: 0,
  charactersRemaining: diagnosticLimits.contextCharacters,
});

const privateField =
  /(?:^|_)(?:token|secret|password|passwd|authorization|cookie|api_key|private_?keys?|credential|username|email|title|filename|filepath|path|url|uri|body|payload|headers|attachment)(?:_|$)|^(?:response|request)$/i;

export function privateDiagnosticField(key: string): boolean {
  return privateField.test(
    key
      .replace(/^\$+/, "")
      .replace(/([a-z])([A-Z])/g, "$1_$2")
      .replace(/[.-]/g, "_")
  );
}
// Apply the same field-sensitive policy at every diagnostic object boundary.
// Structured technical metadata survives; opaque content strings/string arrays do not.
export function privateDiagnosticValue(key: string, value: unknown): boolean {
  if (privateDiagnosticField(key)) return true;
  if (!/^(?:input|content|variables|args|data)$/i.test(key)) return false;
  if (typeof value === "string") return true;
  if (!Array.isArray(value)) return false;
  try {
    for (
      let index = 0;
      index < Math.min(value.length, diagnosticLimits.arrayItems);
      index += 1
    ) {
      const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
      if (
        descriptor &&
        "value" in descriptor &&
        typeof descriptor.value === "string"
      )
        return true;
    }
    return false;
  } catch {
    // An uninspectable opaque-content container cannot be safely exported.
    return true;
  }
}

export function diagnosticMessage(
  value: unknown,
  accounting?: IDiagnosticAccounting
): string {
  if (typeof value !== "string") return "Non-string error message [redacted]";
  // Redact before truncation, so a cut-off token is never emitted as a prefix.
  // Decoding also exposes percent-encoded credentials/paths to the same rules.
  let message = value.slice(0, diagnosticLimits.inspectedCharacters);
  const inspectionTruncated = message.length !== value.length;
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
    if (accounting) accounting.redacted += 1;
    return `Invalid JSON${
      position
        ? ` at position ${position[1]}${
            position[3] ? ` line ${position[2]} column ${position[3]}` : ""
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
      /(^|[^\w.-])(["']?(?:[\w.-]*(?:token|secret|password|passwd|api[_-]?key|authorization|cookie|credential)[\w.-]*)["']?[ \t]*[:=][ \t]*)(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\r\n]+)/gi,
      "$1$2[credential redacted]"
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
    .replace(
      /(["'])(?:[A-Z]:[\\/]|\\\\|\/|~\/)[^"'\n]*(?:\1|$)/gi,
      "[path redacted]"
    )
    .replace(
      /(["'])[^"'\n]+\.(?:mp4|mkv|mov|avi|webm|mp3|wav|flac|jpg|jpeg|png|gif|webp|funscript)\1/gi,
      "[media redacted]"
    )
    .replace(
      /\b[A-Z]:[\\/][^\s<>"']+|\\\\[^\s<>"']+|(?:\/|~\/)[^\s<>"'()\/]+(?:\/[^\s<>"'),;]+)+/gi,
      "[path redacted]"
    )
    .replace(
      /(^|[\s<>"'()])[^\s<>"'()]+\.(?:mp4|mkv|mov|avi|webm|mp3|wav|flac|jpg|jpeg|png|gif|webp|funscript)(?:\?[^\s<>"']*)?\b/gi,
      "$1[media redacted]"
    )
    .replace(
      /(^|[^A-Z0-9._%+-])[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi,
      "$1[email redacted]"
    );
  if (
    accounting &&
    message !== value.slice(0, diagnosticLimits.inspectedCharacters)
  )
    accounting.redacted += 1;
  if (message.length > diagnosticLimits.message || inspectionTruncated) {
    if (accounting) accounting.truncated += 1;
    return `${message.slice(0, diagnosticLimits.message)} [truncated]`;
  }
  return message;
}

// A shared event budget bounds caller-controlled containers. Inspect descriptors
// rather than evaluating getters; cycles and omitted/truncated values are counted.
export function diagnosticContext(
  value: unknown,
  accounting = diagnosticAccounting()
): unknown {
  return sanitizeContext(value, 0, accounting, new Set<object>());
}

export function diagnosticEntries(
  value: unknown,
  accounting: IDiagnosticAccounting
): [string, unknown][] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [];
  try {
    const entries: [string, unknown][] = [];
    let visited = 0;
    for (const key in value) {
      if (
        ++visited > diagnosticLimits.inspectedEntries ||
        accounting.inspected >= diagnosticLimits.inspectedProperties
      ) {
        accounting.truncated += 1;
        break;
      }
      accounting.inspected += 1;
      if (!/^[A-Za-z_$][\w.$-]{0,79}$/.test(key)) {
        accounting.omitted += 1;
        continue;
      }
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (!descriptor) continue;
      if (!("value" in descriptor)) {
        accounting.accessors += 1;
        continue;
      }
      entries.push([key, descriptor.value]);
    }
    return entries;
  } catch {
    accounting.omitted += 1;
    return [];
  }
}

function sanitizeContext(
  value: unknown,
  depth: number,
  accounting: IDiagnosticAccounting,
  ancestors: Set<object>
): unknown {
  if (--accounting.remaining < 0) {
    accounting.truncated += 1;
    return "[context truncated]";
  }
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "number") {
    if (Number.isFinite(value)) return value;
    accounting.omitted += 1;
    return undefined;
  }
  if (typeof value === "string") {
    const message = diagnosticMessage(value, accounting);
    if (message.length > accounting.charactersRemaining) {
      accounting.truncated += 1;
      // A partial private-redaction marker is harmless; credentials were removed
      // before this shared text budget. Keep separate cause-message capacity.
      const retained = message.slice(
        0,
        Math.max(0, accounting.charactersRemaining)
      );
      accounting.charactersRemaining = 0;
      return `${retained} [context text truncated]`;
    }
    accounting.charactersRemaining -= message.length;
    return message;
  }
  if (!value || typeof value !== "object") {
    accounting.omitted += 1;
    return undefined;
  }
  if (ancestors.has(value)) {
    accounting.cycles += 1;
    return "[circular context omitted]";
  }
  if (depth >= diagnosticLimits.depth) {
    accounting.truncated += 1;
    return "[context truncated]";
  }
  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      if (value.length > diagnosticLimits.arrayItems) accounting.truncated += 1;
      const result: unknown[] = [];
      for (
        let n = 0;
        n < Math.min(value.length, diagnosticLimits.arrayItems);
        n += 1
      ) {
        const item = Object.getOwnPropertyDescriptor(value, String(n));
        if (item && !("value" in item)) {
          accounting.accessors += 1;
          result.push(null);
        } else
          result.push(
            sanitizeContext(item?.value, depth + 1, accounting, ancestors) ??
              null
          );
      }
      return result;
    }
    if (![Object.prototype, null].includes(Object.getPrototypeOf(value))) {
      accounting.omitted += 1;
      return undefined;
    }
    const result: Record<string, unknown> = {};
    for (const [key, item] of diagnosticEntries(value, accounting)) {
      if (Object.keys(result).length >= diagnosticLimits.objectFields) {
        accounting.truncated += 1;
        break;
      }
      if (privateDiagnosticValue(key, item)) {
        accounting.redacted += 1;
        Object.defineProperty(result, key, {
          value: "[private content redacted]",
          enumerable: true,
        });
        continue;
      }
      const safe = sanitizeContext(item, depth + 1, accounting, ancestors);
      if (safe !== undefined)
        Object.defineProperty(result, key, { value: safe, enumerable: true });
    }
    return result;
  } catch {
    accounting.omitted += 1;
    return undefined;
  } finally {
    ancestors.delete(value);
  }
}
