package build

import "testing"

func setVersionIdentity(t *testing.T, release, revision, stamp, official string) {
	t.Helper()
	oldVersion, oldHash, oldStamp, oldOfficial := version, githash, buildstamp, officialBuild
	t.Cleanup(func() {
		version, githash, buildstamp, officialBuild = oldVersion, oldHash, oldStamp, oldOfficial
	})
	version, githash, buildstamp, officialBuild = release, revision, stamp, official
}

func TestIsDevelop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		githash string
		develop bool
	}{
		{"fork development", "0.1.0-dev+sha.012345abcdef", "012345abcdef", true},
		{"fork multi-digit version", "12.34.56-dev+sha.abcdef012345", "abcdef012345", true},
		{"fork zero version", "0.0.0-dev+sha.000000000000", "000000000000", true},
		{"legacy development", "v0.28.0-42-gabcdef0", "abcdef0", true},
		{"legacy release candidate development", "v0.28.0-rc1-1-gabcdef0", "abcdef0", true},
		{"release", "0.1.0", "012345abcdef", false},
		{"legacy release", "v0.28.0", "abcdef0", false},
		{"release candidate", "0.1.0-rc.1", "012345abcdef", false},
		{"missing revision", "0.1.0-dev+sha.012345abcdef", "", false},
		{"legacy missing revision", "v0.28.0-42-gabcdef0", "", false},
		{"empty version", "", "012345abcdef", false},
		{"leading zero major", "00.1.0-dev+sha.012345abcdef", "012345abcdef", false},
		{"leading zero minor", "0.01.0-dev+sha.012345abcdef", "012345abcdef", false},
		{"leading zero patch", "0.1.00-dev+sha.012345abcdef", "012345abcdef", false},
		{"missing patch", "0.1-dev+sha.012345abcdef", "012345abcdef", false},
		{"short revision", "0.1.0-dev+sha.abcdef0", "abcdef0", false},
		{"long revision", "0.1.0-dev+sha.012345abcdef0", "012345abcdef0", false},
		{"non-hex revision", "0.1.0-dev+sha.012345abcdeg", "012345abcdeg", false},
		{"upper-case revision", "0.1.0-dev+sha.012345ABCDEF", "012345ABCDEF", false},
		{"other prerelease", "0.1.0-rc+sha.012345abcdef", "012345abcdef", false},
		{"missing metadata", "0.1.0-dev", "012345abcdef", false},
		{"trailing text", "0.1.0-dev+sha.012345abcdef.extra", "012345abcdef", false},
		{"prefixed text", "release-0.1.0-dev+sha.012345abcdef", "012345abcdef", false},
		{"trailing newline", "0.1.0-dev+sha.012345abcdef\n", "012345abcdef", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setVersionIdentity(t, tc.version, tc.githash, "", "")
			if got := IsDevelop(); got != tc.develop {
				t.Fatalf("IsDevelop() = %v, want %v for %q", got, tc.develop, tc.version)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		githash  string
		stamp    string
		official string
		want     string
	}{
		{"local development", "0.1.0-dev+sha.012345abcdef", "012345abcdef", "", "", "Vexxx Edition 0.1.0-dev+sha.012345abcdef - Unofficial Build"},
		{"official development", "0.1.0-dev+sha.012345abcdef", "012345abcdef", "2026-10-06 00:00:00", "true", "Vexxx Edition 0.1.0-dev+sha.012345abcdef - Official Build - 2026-10-06 00:00:00"},
		{"official release", "0.1.0", "012345abcdef", "", "true", "Vexxx Edition 0.1.0 (012345abcdef) - Official Build"},
		{"local release", "0.1.0", "012345abcdef", "", "", "Vexxx Edition 0.1.0 (012345abcdef) - Unofficial Build"},
		{"legacy development", "v0.28.0-42-gabcdef0", "abcdef0", "", "true", "Vexxx Edition v0.28.0-42-gabcdef0 - Official Build"},
		{"legacy release", "v0.28.0", "abcdef0", "", "true", "Vexxx Edition v0.28.0 (abcdef0) - Official Build"},
		{"unversioned commit", "", "012345abcdef", "", "", "Vexxx Edition 012345abcdef - Unofficial Build"},
		{"unknown build", "", "", "", "", "Vexxx Edition unknown - Unofficial Build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setVersionIdentity(t, tc.version, tc.githash, tc.stamp, tc.official)
			if got := VersionString(); got != tc.want {
				t.Fatalf("VersionString() = %q, want %q", got, tc.want)
			}
		})
	}
}
