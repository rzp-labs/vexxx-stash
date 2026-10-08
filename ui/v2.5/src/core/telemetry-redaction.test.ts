import { describe, expect, it } from "vitest";
import { diagnosticContext, diagnosticMessage } from "./telemetry-redaction";

describe("diagnostics with targeted private data redaction", () => {
  it.each([
    "Cannot read properties of undefined (reading 'decodeFrame')",
    "Cannot set properties of null (setting 'selectedIndex')",
    "player.resumePlayback is not a function",
    "privateTitle is not defined",
    "Failed to initialize WebGL2 renderer: EXT_color_buffer_float unavailable",
    "Decoder queue stalled after 8 frames; retry budget exhausted",
    "Loading chunk 172 failed",
    "[HandyAPIv3] hspAdd: points must be an array",
    "useSettings must be used within a SettingsContext",
    "Install PythonTools failed: python-tools@2.5.1 requires numpy>=2.0",
    "Package av==12.0.0 is incompatible with Python 3.13",
    "Install @vendor/python-tools@2.5.1 failed: dependency resolution exhausted",
  ])(
    "retains unfamiliar technical cause and property identifiers: %s",
    (message) => {
      expect(diagnosticMessage(message)).toBe(message);
    }
  );

  it.each([
    [
      "HTTP 401: token=secret private-user /media/private.mp4",
      "HTTP 401 [response redacted]",
    ],
    [
      "Backend error: 503 private title",
      "Backend error 503 [response redacted]",
    ],
    [
      "Auth failed (HTTP 403): Bearer secret",
      "Auth failed (HTTP 403) [response redacted]",
    ],
    [
      "Failed to fetch sprite: private@example.test",
      "Failed to fetch sprite: [response redacted]",
    ],
    [
      "Unexpected token 'p', \"private media\" is not valid JSON",
      "Invalid JSON [input redacted]",
    ],
    [
      "Unexpected non-whitespace character after JSON at position 25 (line 1 column 26)",
      "Invalid JSON at position 25 column 26 [input redacted]",
    ],
    [
      "Loading chunk 172 failed: https://user:password@private.example/assets/index.js?token=secret",
      "Loading chunk 172 failed: [URL redacted]",
    ],
    [
      "Decoder failed for /mnt/media/private.mp4",
      "Decoder failed for [path redacted]",
    ],
    [
      "Decoder failed for C:\\Users\\Private\\media.mp4",
      "Decoder failed for [path redacted]",
    ],
    ["Decoder failed for private.mp4", "Decoder failed for [media redacted]"],
    [
      "Session rejected for private@example.test",
      "Session rejected for [email redacted]",
    ],
    [
      "Renderer failed; response body: Jane Smith and private title",
      "Renderer failed; [content redacted]",
    ],
    [
      'Renderer failed; {"title":"Jane Smith","data":"private"}',
      "Renderer failed; [content redacted]",
    ],
  ])(
    "keeps surrounding diagnostics, removes sensitive slots",
    (message, expected) => {
      expect(diagnosticMessage(message)).toBe(expected);
    }
  );

  it.each([
    "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJwcml2YXRlIn0.secret",
    "Basic dXNlcjpwYXNz",
    "access_token=secret",
    'apiKey="secret with spaces"',
    "password='secret with spaces'",
    "ghp_abcdef0123456789",
    "https%3A%2F%2Fuser%3Asecret%40private.example%2Fmedia.mp4%3Ftoken%3Dsecret",
    "https%253A%252F%252Fuser%253Asecret%2540private.example%252Fmedia.mp4",
    'title="Jane Smith"',
    "file:///Users/private/movie.mp4",
    "100% ready https%3A%2F%2Fuser%3Asecret%40private.example%2Fmedia.mp4",
    '"C:\\Users\\Jane Smith\\private movie.mp4"',
    '"My private movie.mp4"',
    "response: Jane Smith",
    '{"title":"Jane Smith and secret"',
    "Authorization: Bearer secret",
  ])("redacts embedded credentials/private values: %s", (privateValue) => {
    const result = diagnosticMessage(
      `Playback initialization failed: ${privateValue}`
    );
    expect(result).toContain("Playback initialization failed:");
    expect(result).not.toMatch(
      /secret|private.example|Jane Smith|My private|abcdef0123456789|dXNlcjpwYXNz/
    );
  });

  it.each([
    [
      "Client failed: Cookie: theme=dark; sid=opaque-session-value",
      "Client failed: Cookie: [credential redacted]",
    ],
    [
      "Client failed: Set-Cookie: sid=opaque-session-value; HttpOnly; Secure",
      "Client failed: Set-Cookie: [credential redacted]",
    ],
    [
      "Client failed: Cookie: theme=dark; sid=opaque-session-value\nRetry budget exhausted",
      "Client failed: Cookie: [credential redacted]\nRetry budget exhausted",
    ],
    [
      "Client failed: Cookie:\nRetry budget exhausted",
      "Client failed: Cookie:[credential redacted]\nRetry budget exhausted",
    ],
    [
      "Auth failed: password=\nRetry budget exhausted",
      "Auth failed: password=\nRetry budget exhausted",
    ],
    [
      "Auth failed: password=correct horse battery staple",
      "Auth failed: password=[credential redacted]",
    ],
    [
      "Auth failed: password=first-part, second-part; third-part\nRetry budget exhausted",
      "Auth failed: password=[credential redacted]\nRetry budget exhausted",
    ],
    [
      'Auth failed: password="correct horse battery staple"; retries=2',
      "Auth failed: password=[credential redacted]; retries=2",
    ],
    [
      "Auth failed: password=[correct horse] battery staple",
      "Auth failed: password=[credential redacted]",
    ],
    [
      'Auth failed: password="correct \\"horse\\" battery staple"; retries=2',
      "Auth failed: password=[credential redacted]; retries=2",
    ],
    [
      "Auth failed: cookie=theme=dark; sid=opaque-session-value",
      "Auth failed: cookie=[credential redacted]",
    ],
  ])(
    "redacts complete known headers and credential assignments",
    (message, expected) => {
      expect(diagnosticMessage(message)).toBe(expected);
    }
  );

  it.each([
    "PRIVATE KEY",
    "RSA PRIVATE KEY",
    "EC PRIVATE KEY",
    "OPENSSH PRIVATE KEY",
    "ENCRYPTED PRIVATE KEY",
  ])("redacts complete PEM %s blocks before truncation", (type) => {
    const pem = `-----BEGIN ${type}-----\n${"c2VlZGVkLXByaXZhdGUta2V5".repeat(
      500
    )}\n-----END ${type}-----`;
    expect(
      diagnosticMessage(
        `TLS initialization failed: ${pem}\nPackage cryptography==43.0.0`
      )
    ).toBe(
      "TLS initialization failed: [private key redacted]\nPackage cryptography==43.0.0"
    );
    expect(
      diagnosticMessage(
        `TLS initialization failed: -----BEGIN ${type}-----\nprivate-key-fragment`
      )
    ).toBe("TLS initialization failed: [private key redacted]");
  });

  it.each([
    "privateKey",
    "private_key",
    "rsaPrivateKey",
    "private-key",
    "privateKeyPem",
    "privatekey",
    "privateKeys",
    "client_private_key",
  ])(
    "redacts nested private-key field %s without suppressing package identifiers",
    (key) => {
      expect(
        diagnosticContext({
          package: "python-tools@2.5.1",
          nested: { [key]: "opaque-key-material" },
        })
      ).toEqual({
        package: "python-tools@2.5.1",
        nested: { [key]: "[private content redacted]" },
      });
    }
  );

  it.each([
    "旅行.mp4",
    "фильм.mkv",
    "🎬.webm",
    "e\u0301pisode.mov",
    "媒体/旅行.mp4",
    "/媒体/旅行.mp4",
    '"我的 私人影片.mp4"',
  ])("redacts the complete Unicode media filename %s", (filename) => {
    const result = diagnosticMessage(
      `Decoder failed for ${filename}; package av==12.0.0`
    );
    expect(result).toMatch(
      /^Decoder failed for \[(?:media|path) redacted\]; package av==12.0.0$/
    );
    expect(result).not.toMatch(/旅行|фильм|🎬|pisode|媒体|私人/);
  });

  it("does not mistake private JSON preview digits for parser position", () => {
    expect(
      diagnosticMessage(
        `Unexpected token 'a', "at position 9876" is not valid JSON`
      )
    ).toBe("Invalid JSON [input redacted]");
  });
  it("redacts before bounding message size", () => {
    const result = diagnosticMessage(
      `Queue failed: token=${"a".repeat(10000)} exhausted`
    );
    expect(result).toBe("Queue failed: token=[credential redacted]");
    expect(diagnosticMessage("x".repeat(10000))).toHaveLength(2060);
    expect(diagnosticMessage(undefined)).toBe(
      "Non-string error message [redacted]"
    );
  });
  it("keeps typed nested context, redacts raw content, and bounds cycles", () => {
    const context = {
      decoder: {
        codec: "av1",
        ready: false,
        pending: 8,
        securityMode: "strict",
        responseStatus: 503,
      },
      response_body: { title: "Jane Smith" },
      headers: { Authorization: "Bearer secret" },
      filename: "private.mp4",
      message: "Queue stalled: token=secret",
      history: ["retry", 2],
      nan: NaN,
      unsupported: new Error("private"),
      cycle: {},
    };
    context.cycle = context;
    const safe = diagnosticContext(context);
    expect(safe).toMatchObject({
      decoder: {
        codec: "av1",
        ready: false,
        pending: 8,
        securityMode: "strict",
        responseStatus: 503,
      },
      response_body: "[private content redacted]",
      headers: "[private content redacted]",
      message: "Queue stalled: token=[credential redacted]",
      history: ["retry", 2],
    });
    expect(safe).not.toHaveProperty("nan");
    expect(safe).not.toHaveProperty("unsupported");
    expect(JSON.stringify(safe)).not.toMatch(
      /Jane Smith|Bearer secret|private.mp4/
    );
    expect(JSON.stringify(safe)).toContain("[context truncated]");
    expect(
      diagnosticContext(Array.from({ length: 100 }, () => "retry"))
    ).toHaveLength(20);
    expect(
      Object.keys(
        diagnosticContext(
          Object.fromEntries(
            Array.from({ length: 100 }, (_, n) => [`field${n}`, n])
          )
        ) as object
      )
    ).toHaveLength(30);
  });
});
