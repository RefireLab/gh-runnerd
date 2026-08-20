// Package runnerimages turns GitHub's actions/runner-images repository —
// the Packer sources GitHub uses to build its hosted ubuntu-latest VMs —
// into a provisioning plan gh-runnerd can execute inside its own bake VM.
// No Packer or Azure needed: the same shell/pwsh build scripts run in the
// QEMU golden image, so `runs-on: gh-runnerd` gets the tools workflows
// expect (git, gh, node, python, docker, ...).
package runnerimages

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Flavor selects how much of the GitHub-hosted software set is baked in.
type Flavor string

const (
	// FlavorMinimal is the classic gh-runnerd image: Docker + runner only.
	FlavorMinimal Flavor = "minimal"
	// FlavorEssential runs a curated subset of the upstream build scripts:
	// the everyday tools (git, gh, node, python, cmake, docker, ...).
	FlavorEssential Flavor = "essential"
	// FlavorFull runs every upstream build script — the whole
	// ubuntu-latest kitchen sink. Tens of gigabytes, hours of baking.
	FlavorFull Flavor = "full"
)

// ParseFlavor validates a user-supplied flavor name.
func ParseFlavor(s string) (Flavor, error) {
	switch Flavor(strings.ToLower(strings.TrimSpace(s))) {
	case "", FlavorMinimal:
		return FlavorMinimal, nil
	case FlavorEssential:
		return FlavorEssential, nil
	case FlavorFull:
		return FlavorFull, nil
	default:
		return "", fmt.Errorf("unknown image flavor %q (minimal, essential, or full)", s)
	}
}

const (
	// Repo is the upstream image definition repository.
	Repo = "actions/runner-images"
)

var familyRe = regexp.MustCompile(`^ubuntu-(\d{2})\.(\d{2})$`)

// ValidFamily reports whether name looks like an upstream Ubuntu image
// name such as "ubuntu-24.04".
func ValidFamily(name string) bool {
	return familyRe.MatchString(name)
}

// KnownFamilies is the static fallback list of upstream Ubuntu images,
// newest first. Live discovery (DiscoverFamilies / LatestReleases) is
// preferred when the network cooperates.
func KnownFamilies() []string {
	return []string{"ubuntu-24.04", "ubuntu-26.04", "ubuntu-22.04"}
}

// UbuntuVersion extracts "24.04" from "ubuntu-24.04".
func UbuntuVersion(family string) string {
	return strings.TrimPrefix(family, "ubuntu-")
}

// ImageOS returns the ImageOS value GitHub sets on hosted runners:
// ubuntu-24.04 -> ubuntu24.
func ImageOS(family string) string {
	m := familyRe.FindStringSubmatch(family)
	if m == nil {
		return ""
	}
	return "ubuntu" + m[1]
}

// TagPrefix maps a family+arch to the upstream release tag prefix:
// ubuntu-24.04/amd64 -> "ubuntu24/", ubuntu-24.04/arm64 -> "ubuntu24-arm64/".
func TagPrefix(family, arch string) string {
	m := familyRe.FindStringSubmatch(family)
	if m == nil {
		return ""
	}
	if arch == "arm64" {
		return "ubuntu" + m[1] + "-arm64/"
	}
	return "ubuntu" + m[1] + "/"
}

// TemplateFile is the Packer template basename for a family+arch:
// build.ubuntu-24_04.pkr.hcl or build.ubuntu-24_04-arm64.pkr.hcl.
func TemplateFile(family, arch string) string {
	base := "build." + strings.ReplaceAll(family, ".", "_")
	if arch == "arm64" {
		base += "-arm64"
	}
	return base + ".pkr.hcl"
}

// ToolsetFile is the toolset JSON basename for a family+arch:
// toolset-2404.json or toolset-2404-arm64.json.
func ToolsetFile(family, arch string) string {
	m := familyRe.FindStringSubmatch(family)
	if m == nil {
		return ""
	}
	name := "toolset-" + m[1] + m[2]
	if arch == "arm64" {
		name += "-arm64"
	}
	return name + ".json"
}

var templateFileRe = regexp.MustCompile(`^build\.(ubuntu-\d{2}_\d{2})\.pkr\.hcl$`)

// DiscoverFamilies lists the Ubuntu image families present in an extracted
// runner-images tree (root contains images/ubuntu/...). arm64 variants use
// the same family names, so only the non-arm templates are scanned.
func DiscoverFamilies(root string) ([]string, error) {
	dir := filepath.Join(root, "images", "ubuntu", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list upstream templates: %w", err)
	}
	var families []string
	for _, e := range entries {
		m := templateFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		families = append(families, strings.ReplaceAll(m[1], "_", "."))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(families)))
	if len(families) == 0 {
		return nil, fmt.Errorf("no ubuntu templates found under %s", dir)
	}
	return families, nil
}

// Recommended bake resources per flavor. Values are floors: the
// configured vm.disk / vm.memory win when they are already larger.

// RecommendedDiskGB is the virtual disk size the golden image needs.
func RecommendedDiskGB(f Flavor) int {
	switch f {
	case FlavorEssential:
		return 60
	case FlavorFull:
		return 150
	case FlavorMinimal:
		return 0
	default:
		return 0
	}
}

// RecommendedMemoryMB is the bake VM RAM floor.
func RecommendedMemoryMB(f Flavor) int {
	switch f {
	case FlavorEssential:
		return 4096
	case FlavorFull:
		return 8192
	case FlavorMinimal:
		return 0
	default:
		return 0
	}
}

// RecommendedTimeout is the default bake timeout.
func RecommendedTimeout(f Flavor) time.Duration {
	switch f {
	case FlavorEssential:
		return 3 * time.Hour
	case FlavorFull:
		return 14 * time.Hour
	case FlavorMinimal:
		return 0
	default:
		return 0
	}
}

// EstimatedDataGB is roughly how much host disk the bake will consume
// (image data plus the compressed copy) — used for the free-space check.
func EstimatedDataGB(f Flavor) int {
	switch f {
	case FlavorEssential:
		return 30
	case FlavorFull:
		return 130
	case FlavorMinimal:
		return 10
	default:
		return 10
	}
}
