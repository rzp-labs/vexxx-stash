// Error text can contain server responses, media titles or arbitrary user input.
// Keep source-owned diagnostics and native error grammar; redact data-bearing
// leaves rather than forwarding unknown free text through a token blacklist.
// Known code properties; a syntactically valid identifier can also be a private
// user-supplied object key, so grammar alone is insufficient for these slots.
const codeNames = new Set([
  "duration",
  "currentTime",
  "length",
  "map",
  "filter",
  "reduce",
  "forEach",
  "find",
  "includes",
  "split",
  "replace",
  "trim",
  "toString",
  "then",
  "catch",
  "play",
  "pause",
  "load",
  "dispose",
  "src",
  "type",
  "value",
  "id",
  "data",
  "errors",
  "message",
  "stack",
  "name",
  "prototype",
  "constructor",
  "call",
  "apply",
  "bind",
  "push",
  "slice",
  "indexOf",
  "children",
  "props",
  "state",
  "undefined",
  "null",
  "posthog",
  "videojs",
  "React",
  "window",
  "document",
]);
const staticMessages = new Set([
  "Maximum call stack size exceeded",
  "Invalid array length",
  "Invalid time value",
  "Unexpected end of JSON input",
  "Failed to fetch",
  "NetworkError when attempting to fetch resource.",
  "Load failed",
  "Script error.",
  "ResizeObserver loop limit exceeded",
  "ResizeObserver loop completed with undelivered notifications.",
  "Not a valid funscript",
  "Handy not connected",
  "No token received from auth endpoint",
  "Local Handy bridge connection failed",
  "Local Handy bridge disconnected",
  "Local Handy bridge error",
  "Failed to synchronize server time after all samples.",
  "[HandyAPIv3] hspAdd: points must be an array",
  "Failed to generate sprite info",
  "Failed to create tag",
  "Failed to create or find group",
  "Failed to fetch VTT file",
  "Failed to analyze scene",
  "Failed to fetch original scenes",
  "Could not find performer to remove",
  "Could not find group to remove",
  "Could not find tag to remove",
  "performer name must set",
  "studio name must set",
  "parent studio name must set",
  "unexpected tagOperation",
  "Invalid modifier value",
  "VRCanvasPanel: 2D canvas context unavailable",
  "useToast must be used within a ToastProvider",
  "useSettings must be used within a SettingsContext",
  "useFilter must be used within a FilterStateContext",
  "useListContext must be used within a ListStateContext",
]);

export function diagnosticMessage(value: unknown): string {
  if (typeof value !== "string") return "Non-string error message [redacted]";
  if (staticMessages.has(value)) return value;

  // These slots describe executable identifiers, not values. Reject filenames,
  // URLs and paths even when they appear inside an otherwise native message.
  const property = value.match(
    /^(Cannot (?:read|set) properties of (?:undefined|null)) \((reading|setting) ['"]([^'"]+)['"]\)$/
  );
  if (property)
    return `${property[1]} (${property[2]} '${
      codeNames.has(property[3]) ? property[3] : "[property redacted]"
    }')`;
  const identifier = value.match(
    /^([A-Za-z_$][\w.$]{0,120}) (is not (?:a function|defined|a constructor)|is undefined|is null)$/
  );
  if (identifier)
    return `${
      codeNames.has(identifier[1]) ? identifier[1] : "[identifier redacted]"
    } ${identifier[2]}`;
  // Keep HTTP status and operation while dropping response bodies/status text.
  const http = value.match(
    /^(HTTP|Backend error|funscript fetch failed|Script upload failed \(HTTP|Auth failed \(HTTP)[ :]+([1-5]\d{2})(?:\))?(?::.*)?$/s
  );
  if (http)
    return `${http[1]} ${http[2]}${
      http[1].includes("(") ? ")" : ""
    } [response redacted]`;
  const fetch = value.match(
    /^(Failed to fetch (?:sprite|VTT|image)|Status check failed): (.*)$/s
  );
  if (fetch)
    return `${fetch[1]}: ${
      /^[1-5]\d{2}$/.test(fetch[2]) ? fetch[2] : "[response redacted]"
    }`;
  if (/^Unexpected (?:token|non-whitespace character).*JSON/.test(value)) {
    // A preview can itself contain "at position 1234". Only parse a complete
    // native grammar that has no input-preview slot.
    const position = value.match(
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
  if (
    /^(?:Failed to fetch dynamically imported module|Importing a module script failed|Loading chunk [0-9]+ failed)/.test(
      value
    )
  )
    return "JavaScript chunk load failed [URL redacted]";
  return "Unrecognized error text [redacted]";
}
