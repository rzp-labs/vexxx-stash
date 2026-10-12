import React, { useCallback, useEffect, useRef, useState } from "react";
import posthogClient from "posthog-js/no-external";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  TextField,
  Typography,
} from "@mui/material";
import { Icon } from "../Shared/Icon";
import { LoadingIndicator } from "../Shared/LoadingIndicator";
import { SettingSection } from "./SettingSection";
import { Setting } from "./Inputs";
import {
  faCopy,
  faDownload,
  faGlobe,
  faPlay,
  faStop,
  faSyncAlt,
} from "@fortawesome/free-solid-svg-icons";

interface ITLSStatus {
  enabled: boolean;
  running: boolean;
  port: number;
  urls: string[];
  error?: string | null;
}

function errMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
}

// Each keyed URL owns its request/feedback, so removed URLs cannot report stale
// success when HTTPS is disabled, refreshed or the settings panel is closed.
const LocalHTTPSURL: React.FC<{ url: string }> = ({ url }) => {
  const input = useRef<HTMLInputElement>(null);
  const active = useRef(true);
  const pending = useRef(false);
  const [copying, setCopying] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);

  const selectURL = () => {
    input.current?.focus();
    input.current?.select();
  };

  const copyURL = async () => {
    if (pending.current) return;
    setCopied(false);
    setCopyError(null);
    try {
      if (typeof navigator.clipboard?.writeText !== "function") {
        setCopyError(
          "Automatic copying is unavailable in this browser or connection. Select the URL and copy it manually."
        );
        selectURL();
        return;
      }
      pending.current = true;
      setCopying(true);
      // Invoke directly from the click, preserving the browser's user activation.
      await navigator.clipboard.writeText(url);
      if (active.current) setCopied(true);
    } catch (error) {
      const denied =
        error instanceof DOMException && error.name === "NotAllowedError";
      if (!denied) {
        console.error("Local HTTPS URL copy failed", error);
        try {
          posthogClient.captureException(error, {
            operation: "clipboard.write_text",
            component: "SettingsLocalHTTPSPanel",
          });
        } catch (captureError) {
          console.error("Could not report clipboard error", captureError);
        }
      }
      if (active.current) {
        setCopyError(
          denied
            ? "Clipboard access was denied. Select the URL and copy it manually."
            : `Could not copy the URL: ${errMessage(
                error,
                "Unknown error"
              )}. Select the URL and copy it manually.`
        );
      }
    } finally {
      pending.current = false;
      if (active.current) setCopying(false);
    }
  };

  return (
    <Box>
      <Box
        sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}
      >
        <Icon icon={faGlobe} />
        <TextField
          size="small"
          label="Headset URL"
          value={url}
          inputRef={input}
          InputProps={{ readOnly: true }}
          inputProps={{
            onFocus: (event: React.FocusEvent<HTMLInputElement>) =>
              event.currentTarget.select(),
          }}
          sx={{ flexGrow: 1, minWidth: 200 }}
        />
        <Button
          size="small"
          variant="contained"
          color="info"
          startIcon={<Icon icon={faCopy} />}
          onClick={copyURL}
          disabled={copying}
          aria-label={`Copy URL ${url}`}
        >
          {copying ? "Copying…" : "Copy"}
        </Button>
        <Button
          size="small"
          onClick={selectURL}
          aria-label={`Select URL ${url}`}
        >
          Select URL
        </Button>
      </Box>
      <Typography variant="caption" component="p" sx={{ mt: 1 }}>
        To copy manually, select the URL and press Ctrl+C or ⌘C, or touch and
        hold it and choose Copy.
      </Typography>
      <Typography role="status" aria-live="polite" variant="body2">
        {copied ? "URL copied to clipboard." : copying ? "Copying URL…" : ""}
      </Typography>
      {copyError && (
        <Alert severity="warning" sx={{ mt: 1 }}>
          {copyError}
        </Alert>
      )}
    </Box>
  );
};

async function fetchTLSStatus(): Promise<ITLSStatus> {
  const res = await fetch("/tls/status");
  if (!res.ok) throw new Error(`Failed to fetch status: ${res.statusText}`);
  return res.json();
}

async function enableTLS(port: number): Promise<ITLSStatus> {
  const res = await fetch(`/tls/enable?port=${port}`, { method: "POST" });
  if (!res.ok) throw new Error(`Failed to enable: ${res.statusText}`);
  return res.json();
}

async function disableTLS(): Promise<ITLSStatus> {
  const res = await fetch("/tls/disable", { method: "POST" });
  if (!res.ok) throw new Error(`Failed to disable: ${res.statusText}`);
  return res.json();
}

export const SettingsLocalHTTPSPanel: React.FC = () => {
  const [status, setStatus] = useState<ITLSStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [port, setPort] = useState<string>("9443");

  const refresh = useCallback(async () => {
    try {
      const s = await fetchTLSStatus();
      setStatus(s);
      if (s.port) setPort(String(s.port));
      if (s.error) setError(s.error);
    } catch (e) {
      setError(errMessage(e, "Unknown error"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const handleEnable = async () => {
    setActionLoading(true);
    setError(null);
    try {
      const parsed = parseInt(port, 10);
      if (!parsed || parsed < 1 || parsed > 65535) {
        setError("Enter a valid port (1–65535).");
        return;
      }
      const s = await enableTLS(parsed);
      setStatus(s);
      if (s.error) setError(s.error);
    } catch (e) {
      setError(errMessage(e, "Failed to enable Local HTTPS"));
    } finally {
      setActionLoading(false);
    }
  };

  const handleDisable = async () => {
    setActionLoading(true);
    setError(null);
    try {
      setStatus(await disableTLS());
    } catch (e) {
      setError(errMessage(e, "Failed to disable Local HTTPS"));
    } finally {
      setActionLoading(false);
    }
  };

  if (loading && !status) {
    return <LoadingIndicator />;
  }

  const running = !!status?.running;

  return (
    <SettingSection headingID="config.local_https.heading">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Serve Vexxx over HTTPS on a second port using an auto-generated
        certificate, so the in-browser VR player (WebXR, which requires a secure
        context) works on your local network without an external tunnel. Install
        the certificate on your headset once to remove the browser warning.
      </Typography>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Status card */}
      <Paper
        variant="outlined"
        sx={{
          p: 3,
          mb: 3,
          display: "flex",
          alignItems: "center",
          gap: 2,
          bgcolor: running ? "success.dark" : "background.default",
          borderColor: running ? "success.main" : "divider",
        }}
      >
        <Box
          sx={{
            width: 12,
            height: 12,
            borderRadius: "50%",
            bgcolor: running ? "success.light" : "text.disabled",
            boxShadow: running ? "0 0 8px rgba(76, 175, 80, 0.6)" : "none",
            flexShrink: 0,
          }}
        />
        <Box sx={{ flexGrow: 1 }}>
          <Typography variant="subtitle1" fontWeight={600}>
            {running ? "Local HTTPS is active" : "Local HTTPS is off"}
          </Typography>
        </Box>
        {actionLoading && <CircularProgress size={24} />}
      </Paper>

      {/* URLs to open on the headset */}
      {running && status?.urls?.length ? (
        <Paper
          variant="outlined"
          sx={{
            p: 3,
            mb: 3,
            bgcolor: "success.dark",
            borderColor: "success.main",
            borderWidth: 2,
          }}
        >
          <Typography
            variant="overline"
            color="success.light"
            fontWeight={700}
            letterSpacing={1}
          >
            Open one of these on your headset
          </Typography>
          <Box sx={{ display: "flex", flexDirection: "column", gap: 1, mt: 1 }}>
            {status.urls.map((u) => (
              <LocalHTTPSURL key={u} url={u} />
            ))}
          </Box>
          <Button
            variant="contained"
            color="info"
            startIcon={<Icon icon={faDownload} />}
            href="/tls/ca.crt"
            sx={{ mt: 2 }}
          >
            Download certificate for your headset
          </Button>
        </Paper>
      ) : null}

      {/* Port + controls */}
      <Paper variant="outlined" sx={{ p: 3, mb: 3 }}>
        <Box sx={{ display: "flex", alignItems: "flex-start", gap: 2 }}>
          <TextField
            size="small"
            label="HTTPS port"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ""))}
            disabled={running}
            sx={{ width: 160 }}
            helperText="Separate from the main HTTP port"
          />
          <Box sx={{ display: "flex", gap: 2, pt: 0.5 }}>
            {running ? (
              <Button
                variant="contained"
                color="error"
                startIcon={<Icon icon={faStop} />}
                onClick={handleDisable}
                disabled={actionLoading}
              >
                Disable
              </Button>
            ) : (
              <Button
                variant="contained"
                color="success"
                startIcon={<Icon icon={faPlay} />}
                onClick={handleEnable}
                disabled={actionLoading}
              >
                Enable
              </Button>
            )}
            <Button
              variant="outlined"
              startIcon={<Icon icon={faSyncAlt} />}
              onClick={refresh}
              disabled={loading || actionLoading}
            >
              Refresh
            </Button>
          </Box>
        </Box>
        {status?.enabled && (
          <Chip
            label="Starts automatically with Vexxx"
            size="small"
            color="success"
            sx={{ mt: 2 }}
          />
        )}
      </Paper>

      {/* Install instructions */}
      <Typography variant="subtitle2" fontWeight={700} sx={{ mt: 1, mb: 1 }}>
        Setup
      </Typography>
      <Setting heading="1. Enable Local HTTPS">
        Pick a port and click Enable. Vexxx generates a certificate covering
        this machine&apos;s LAN addresses and starts an HTTPS listener.
      </Setting>
      <Setting heading="2. Trust the certificate on your headset">
        On the Quest, open this page, tap “Download certificate for your
        headset”, then install it via Android Settings → Security → Encryption
        &amp; credentials → Install a certificate → CA certificate. (You can
        also just accept the browser warning once, but installing removes it.)
      </Setting>
      <Setting heading="3. Open Vexxx over HTTPS">
        Browse to one of the <code>https://&lt;ip&gt;:{port}</code> URLs above
        on the headset, open a scene, and tap Enter VR. Video now streams at LAN
        speed — no external tunnel.
      </Setting>
    </SettingSection>
  );
};

export default SettingsLocalHTTPSPanel;
