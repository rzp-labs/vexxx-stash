package generate

import (
	"os"
	"path/filepath"
)

type markerTestPaths struct{ dir string }

func (p markerTestPaths) TempFile(pattern string) (*os.File, error) {
	return os.CreateTemp(p.dir, pattern)
}
func (p markerTestPaths) GetVideoPreviewPath(string, int) string {
	return filepath.Join(p.dir, "marker.mp4")
}
func (p markerTestPaths) GetWebpPreviewPath(string, int) string {
	return filepath.Join(p.dir, "marker.webp")
}
func (p markerTestPaths) GetScreenshotPath(string, int) string {
	return filepath.Join(p.dir, "marker.jpg")
}

type webpTestConfig struct{}

func (webpTestConfig) GetTranscodeInputArgs() []string  { return nil }
func (webpTestConfig) GetTranscodeOutputArgs() []string { return nil }
