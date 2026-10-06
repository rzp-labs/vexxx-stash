// Package build provides the version information for the application.
package build

import (
	"regexp"
)

var version string
var buildstamp string
var githash string
var officialBuild string
var releaseRepo string

var legacyDevelopmentVersion = regexp.MustCompile(`-\d+-g\w+$`)
var semanticDevelopmentVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-dev\+sha\.[0-9a-f]{12}$`)

func Version() (string, string, string) {
	return version, githash, buildstamp
}

func ReleaseRepo() string {
	return releaseRepo
}

func VersionString() string {
	var versionString string
	switch {
	case version != "":
		if githash != "" && !IsDevelop() {
			versionString = version + " (" + githash + ")"
		} else {
			versionString = version
		}
	case githash != "":
		versionString = githash
	default:
		versionString = "unknown"
	}
	if IsOfficial() {
		versionString += " - Official Build"
	} else {
		versionString += " - Unofficial Build"
	}
	if buildstamp != "" {
		versionString += " - " + buildstamp
	}
	return "Vexxx Edition " + versionString
}

func IsOfficial() bool {
	return officialBuild == "true"
}

func IsDevelop() bool {
	if githash == "" {
		return false
	}

	// Development versions already include their commit, either in the fork's
	// SemVer build metadata or in the legacy git-describe suffix.
	return semanticDevelopmentVersion.MatchString(version) || legacyDevelopmentVersion.MatchString(version)
}
