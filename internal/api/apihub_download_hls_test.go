package api

import (
	neturl "net/url"
	"testing"
)

// Shape mirrors a Gamma master playlist: variants listed as bare filenames,
// with the CloudFront signature living only in the master URL's query string.
const gammaMaster = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=2200000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2"
163917_01_720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
163917_01_1080p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=14000000,RESOLUTION=3840x2160,CODECS="hvc1.2.4.L150.90,mp4a.40.2"
163917_01_2160p.m3u8
`

const signedMasterURL = "https://streaming-hls.gammacdn.com/fame/48dbbcab/hls/163917_01.m3u8?uh=0240e7cc&cui=38955244&Policy=eyJTdGF0" +
	"ZW1lbnQiOlt7IlJlc291cmNlIjoiLi4uIn1dfQ__&Signature=JateSp154&Key-Pair-Id=APKAYNAXREE5V7FEZ74I"

func TestParseHLSVariantsSortsBestFirst(t *testing.T) {
	variants := parseHLSVariants(gammaMaster, signedMasterURL)
	if len(variants) != 3 {
		t.Fatalf("expected 3 variants, got %d", len(variants))
	}
	want := []int{2160, 1080, 720}
	for i, h := range want {
		if variants[i].height != h {
			t.Errorf("variant %d: got height %d, want %d", i, variants[i].height, h)
		}
	}
}

// The CloudFront policy signs a prefix wildcard (".../hls/163917_01*"), so a
// variant listed as a bare filename is only authorized if it inherits the
// master's query string. Dropping that is a silent 403, so pin it.
func TestParseHLSVariantsInheritsSigningQuery(t *testing.T) {
	variants := parseHLSVariants(gammaMaster, signedMasterURL)
	for _, v := range variants {
		u, err := neturl.Parse(v.url)
		if err != nil {
			t.Fatalf("variant URL %q: %v", v.url, err)
		}
		q := u.Query()
		for _, key := range []string{"uh", "cui", "Policy", "Signature", "Key-Pair-Id"} {
			if q.Get(key) == "" {
				t.Errorf("variant %s lost signing param %q", v.url, key)
			}
		}
	}
}

// A variant carrying its own query string must keep it rather than having the
// master's stomped on top.
func TestResolveVariantURLKeepsOwnQuery(t *testing.T) {
	got, err := resolveVariantURL("https://cdn.example/hls/master.m3u8?token=abc", "v_1080p.m3u8?token=xyz")
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://cdn.example/hls/v_1080p.m3u8?token=xyz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A media playlist has no EXT-X-STREAM-INF lines; callers rely on an empty
// result to mean "hand this URL straight to ffmpeg".
func TestParseHLSVariantsIgnoresMediaPlaylist(t *testing.T) {
	media := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.006,\nseg_00001.ts\n#EXTINF:6.006,\nseg_00002.ts\n#EXT-X-ENDLIST\n"
	if v := parseHLSVariants(media, "https://cdn.example/hls/v_1080p.m3u8"); len(v) != 0 {
		t.Errorf("expected no variants for a media playlist, got %d", len(v))
	}
}

func TestHLSPlaylistDuration(t *testing.T) {
	media := "#EXTM3U\n#EXTINF:6.006,\na.ts\n#EXTINF:6.006,\nb.ts\n#EXTINF:1.988,\nc.ts\n#EXT-X-ENDLIST\n"
	got := hlsPlaylistDuration(media)
	if want := 14.0; got < want-0.01 || got > want+0.01 {
		t.Errorf("got %v, want ~%v", got, want)
	}
}

// CODECS values contain commas; splitting naively would mis-read RESOLUTION.
func TestHLSAttrHandlesQuotedCommas(t *testing.T) {
	attrs := `BANDWIDTH=5000000,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080`
	if got := hlsAttr(attrs, "RESOLUTION"); got != "1920x1080" {
		t.Errorf("RESOLUTION: got %q, want %q", got, "1920x1080")
	}
	if got := hlsAttrInt(attrs, "BANDWIDTH"); got != 5000000 {
		t.Errorf("BANDWIDTH: got %d, want %d", got, 5000000)
	}
}

func TestSourceIsHLS(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  apihubDownloadSource
		want bool
	}{
		{"explicit hls", apihubDownloadSource{URL: "https://x/y", Kind: "hls"}, true},
		{"explicit file wins over extension", apihubDownloadSource{URL: "https://x/y.m3u8", Kind: "file"}, false},
		{"sniffed from extension", apihubDownloadSource{URL: "https://x/y.m3u8?sig=1"}, true},
		{"progressive", apihubDownloadSource{URL: "https://x/y.mp4?sig=1"}, false},
		{"gamma download route", apihubDownloadSource{URL: "https://members.adulttime.com/movieaction/download/1/1080p/mp4?codec=h264"}, false},
	} {
		if got := tc.src.isHLS(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
