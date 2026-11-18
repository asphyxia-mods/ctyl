package main

import (
	"slices"
	"strings"
)

func rebuildReasons(previous, current State) []string {
	if previous.Digest == "" {
		return []string{"first recorded publication"}
	}
	var reasons []string
	javaChanged := previous.Distribution != current.Distribution ||
		strings.SplitN(previous.RuntimeVersion, "+", 2)[0] != strings.SplitN(current.RuntimeVersion, "+", 2)[0] ||
		previous.SourceCommit != current.SourceCommit || previous.SourceRepo != current.SourceRepo
	if javaChanged {
		reasons = append(reasons, "Java runtime/source updated")
	}
	if previous.Distribution == current.Distribution && previous.DistributionVersion != current.DistributionVersion {
		reasons = append(reasons, current.Distribution+" Java package updated")
	}
	if len(previous.Packages) > 0 && len(current.Packages) > 0 && packageVersions(previous, false) != packageVersions(current, false) {
		reasons = append(reasons, "Wolfi dependencies updated")
	}
	if previous.Revision != current.Revision || packageVersions(previous, true) != packageVersions(current, true) {
		reasons = append(reasons, "image/launcher sources updated")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "other image inputs changed")
	}
	if !javaChanged && previous.RuntimeVersion != "" && current.RuntimeVersion != "" {
		if previous.SourceCommit != "" && current.SourceCommit != "" {
			reasons = append(reasons, "Java runtime/source unchanged")
		} else {
			reasons = append(reasons, "Java version unchanged; source comparison unavailable")
		}
	}
	return reasons
}

func packageVersions(s State, local bool) string {
	var packages []string
	for arch, versions := range s.Packages {
		for _, version := range versions {
			name, _, _ := strings.Cut(version, "=")
			if strings.HasPrefix(name, "openjdk-") || strings.HasPrefix(name, "ctyl-jdk-") {
				continue
			}
			if strings.HasPrefix(name, "ctyl-") == local {
				packages = append(packages, arch+":"+version)
			}
		}
	}
	slices.Sort(packages)
	return strings.Join(packages, "\n")
}
