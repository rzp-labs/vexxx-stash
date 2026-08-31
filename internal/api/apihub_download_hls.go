package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/logger"
)

// Adult Time — and any other Gamma property whose plan grants streaming but
// not the member download route — exposes no progressive file at all: its
// media is CloudFront-signed MPEG-TS segments behind an HLS playlist on
// streaming-hls.gammacdn.com. This file is the "download the stream instead"
// path: fetch the playlist, pick the rendition, and let ffmpeg remux the
// segments into the .mp4 the rest of the pipeline (scan, phash, identify)
// already expects.
//
// Nothing here is Adult Time specific. A source is routed through it purely on
// its Kind/URL, so any provider that only ever hands out an m3u8 gets the same
// treatment.

// apihubDownloadSource is one candidate way to obtain an item's video. An item
// carries a primary URL plus an ordered fallback list, and the job walks them
// until one answers (see downloadFromSources). Kind is "hls" for a playlist
// that needs remuxing, and empty (or "file") for a plain progressive fetch.
type apihubDownloadSource struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Kind    string            `json:"kind,omitempty"`
	// Height is the rendition this source represents, when known. For an HLS
	// master playlist it selects which variant to pull; for a media playlist
	// or a progressive file it is informational only.
	Height int `json:"height,omitempty"`
}

// isHLS reports whether this source needs the playlist/remux path rather than
// a straight byte copy. The explicit Kind wins; the URL is only sniffed as a
// fallback so a caller that omits it still lands in the right place.
func (s apihubDownloadSource) isHLS() bool {
	if s.Kind == "hls" {
		return true
	}
	if s.Kind != "" {
		return false
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// hlsVariant is one rendition advertised by a master playlist.
type hlsVariant struct {
	url       string
	height    int
	bandwidth int
}

// fetchPlaylist GETs a playlist and returns its text. Playlists are small (a
// few KB even for a long scene), so reading the whole body is fine; the limit
// only guards against being handed something that isn't a playlist at all.
func fetchPlaylist(ctx context.Context, client *http.Client, src apihubDownloadSource) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range src.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	text := string(body)
	if !strings.Contains(text, "#EXTM3U") {
		return "", fmt.Errorf("not an HLS playlist")
	}
	return text, nil
}

// parseHLSVariants pulls the rendition list out of a master playlist. Returns
// nil (and no error) for a media playlist, which has no EXT-X-STREAM-INF lines
// — callers treat that as "this URL is already the thing to hand ffmpeg".
func parseHLSVariants(playlist, baseURL string) []hlsVariant {
	var out []hlsVariant
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		// The URI is the next non-comment, non-empty line.
		var uri string
		for j := i + 1; j < len(lines); j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" || strings.HasPrefix(candidate, "#") {
				continue
			}
			uri = candidate
			break
		}
		if uri == "" {
			continue
		}

		attrs := line[len("#EXT-X-STREAM-INF:"):]
		v := hlsVariant{bandwidth: hlsAttrInt(attrs, "BANDWIDTH")}
		if res := hlsAttr(attrs, "RESOLUTION"); res != "" {
			if _, h, ok := strings.Cut(res, "x"); ok {
				v.height, _ = strconv.Atoi(h)
			}
		}
		resolved, err := resolveVariantURL(baseURL, uri)
		if err != nil {
			continue
		}
		v.url = resolved
		out = append(out, v)
	}

	// Best first: by height where the playlist declared one, bandwidth otherwise.
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].height != out[b].height {
			return out[a].height > out[b].height
		}
		return out[a].bandwidth > out[b].bandwidth
	})
	return out
}

// resolveVariantURL resolves a variant URI against the master playlist's URL,
// carrying the master's query string over when the variant has none of its own.
//
// That query-string inheritance is the crux of signed-CDN HLS: Gamma signs the
// master with a CloudFront policy whose Resource is a *prefix* wildcard
// (".../hls/163917_01*"), so one signature authorizes every rendition and
// segment under it — but only if they actually carry it. A variant listed as a
// bare filename would otherwise resolve to an unsigned URL and come back 403.
func resolveVariantURL(baseURL, uri string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(ref)
	if resolved.RawQuery == "" {
		resolved.RawQuery = base.RawQuery
	}
	return resolved.String(), nil
}

// hlsAttr reads one attribute out of an EXT-X-STREAM-INF attribute list,
// tolerating both quoted and bare values.
func hlsAttr(attrs, name string) string {
	for _, part := range splitHLSAttrs(attrs) {
		k, v, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), name) {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), "\"")
	}
	return ""
}

func hlsAttrInt(attrs, name string) int {
	n, _ := strconv.Atoi(hlsAttr(attrs, name))
	return n
}

// splitHLSAttrs splits a comma-separated attribute list, ignoring commas inside
// a quoted value — CODECS="avc1.4d401f,mp4a.40.2" is one attribute, not two.
func splitHLSAttrs(attrs string) []string {
	var out []string
	var buf strings.Builder
	inQuote := false
	for _, r := range attrs {
		switch {
		case r == '"':
			inQuote = !inQuote
			buf.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// hlsPlaylistDuration sums the EXTINF durations of a media playlist. This is
// where the progress denominator comes from: the catalog's own runtime isn't
// carried with a download, and it disagrees with the encode often enough
// (trailers, intros) that the playlist is the better source regardless.
// Returns 0 when the playlist declares no segments, which callers treat as
// "indeterminate progress" rather than an error.
func hlsPlaylistDuration(playlist string) float64 {
	var total float64
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		value := line[len("#EXTINF:"):]
		if idx := strings.Index(value, ","); idx >= 0 {
			value = value[:idx]
		}
		if d, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			total += d
		}
	}
	return total
}

// resolveHLSTarget turns a source URL into the media playlist ffmpeg should
// read, plus that playlist's duration. When the URL is a master playlist the
// best variant at or below src.Height is chosen (falling back to the lowest
// when everything exceeds it, matching pickForCap on the plugin side); when it
// is already a media playlist it is used as-is.
func resolveHLSTarget(ctx context.Context, client *http.Client, src apihubDownloadSource) (string, float64, error) {
	playlist, err := fetchPlaylist(ctx, client, src)
	if err != nil {
		return "", 0, fmt.Errorf("fetch playlist: %w", err)
	}

	variants := parseHLSVariants(playlist, src.URL)
	if len(variants) == 0 {
		return src.URL, hlsPlaylistDuration(playlist), nil // already a media playlist
	}

	chosen := variants[0] // sorted best-first
	if src.Height > 0 {
		chosen = variants[len(variants)-1] // everything above the cap: take the smallest
		for _, v := range variants {
			if v.height > 0 && v.height <= src.Height {
				chosen = v
				break
			}
		}
	}
	logger.Infof("[apihub-download] hls: %d variant(s) offered, taking %dp", len(variants), chosen.height)

	mediaPlaylist, err := fetchPlaylist(ctx, client, apihubDownloadSource{URL: chosen.url, Headers: src.Headers})
	if err != nil {
		// Not fatal — ffmpeg reads the variant itself; we only lose the
		// progress denominator.
		logger.Debugf("[apihub-download] hls: variant playlist unreadable for duration (%v); progress will be indeterminate", err)
		return chosen.url, 0, nil
	}
	return chosen.url, hlsPlaylistDuration(mediaPlaylist), nil
}

// downloadHLS remuxes an HLS source into dest via ffmpeg, stream-copying the
// segments (no re-encode) into MP4. Progress is reported as a 0..1 fraction
// against the playlist duration.
func downloadHLS(ctx context.Context, client *http.Client, src apihubDownloadSource, dest string, onProgress func(float64)) error {
	ff := manager.GetInstance().FFMpeg
	if ff == nil {
		return fmt.Errorf("ffmpeg is not configured; cannot download an HLS stream")
	}

	target, duration, err := resolveHLSTarget(ctx, client, src)
	if err != nil {
		return err
	}

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if len(src.Headers) > 0 {
		// ffmpeg takes extra request headers as one CRLF-delimited blob.
		var hdr strings.Builder
		for k, v := range src.Headers {
			fmt.Fprintf(&hdr, "%s: %s\r\n", k, v)
		}
		args = append(args, "-headers", hdr.String())
	}
	args = append(args,
		"-i", target,
		// Stream copy: the segments already hold the exact rendition that was
		// picked, so this is a container change, not a transcode.
		"-c", "copy",
		"-movflags", "+faststart",
		// dest is a .part file, so the muxer can't be inferred from the name.
		"-f", "mp4",
		"-progress", "pipe:1",
		"-nostats",
		"-y", dest,
	)

	cmd := ff.Command(ctx, args)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	// -progress emits key=value lines; out_time_us is the muxed timestamp so
	// far, which against the playlist duration is a true completion fraction.
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || key != "out_time_us" || duration <= 0 || onProgress == nil {
			continue
		}
		us, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		if fraction := us / 1e6 / duration; fraction >= 0 && fraction <= 1 {
			onProgress(fraction)
		}
	}

	if err := cmd.Wait(); err != nil {
		os.Remove(dest)
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg remux failed: %s", msg)
		}
		return fmt.Errorf("ffmpeg remux failed: %w", err)
	}
	return nil
}
