package host

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const minUbuntu = 24.04

// Info is a parsed /etc/os-release.
type Info struct {
	ID        string
	VersionID string
	Pretty    string
}

// ReadOSRelease loads /etc/os-release from disk.
func ReadOSRelease() (Info, error) {
	return ParseOSReleaseFile("/etc/os-release")
}

// ParseOSReleaseFile reads a specific os-release path.
func ParseOSReleaseFile(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	return ParseOSRelease(f)
}

// ParseOSRelease parses os-release contents.
func ParseOSRelease(r interface{ Read([]byte) (int, error) }) (Info, error) {
	info := Info{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			info.ID = strings.ToLower(v)
		case "VERSION_ID":
			info.VersionID = v
		case "PRETTY_NAME":
			info.Pretty = v
		}
	}
	if err := scanner.Err(); err != nil {
		return Info{}, err
	}
	return info, nil
}

// CheckUbuntu returns a hard error when the host is not Ubuntu 24.04+.
func CheckUbuntu(info Info) error {
	if info.ID != "ubuntu" {
		pretty := info.Pretty
		if pretty == "" {
			pretty = info.ID
			if pretty == "" {
				pretty = "unknown"
			}
		}
		return fmt.Errorf("gh-runnerd only runs on Ubuntu 24.04 or newer (detected %s)", pretty)
	}
	v, err := strconv.ParseFloat(info.VersionID, 64)
	if err != nil {
		return fmt.Errorf("cannot parse Ubuntu VERSION_ID %q", info.VersionID)
	}
	if v+1e-9 < minUbuntu {
		return fmt.Errorf("gh-runnerd requires Ubuntu %.2f or newer (detected %s)", minUbuntu, info.VersionID)
	}
	return nil
}

// MustUbuntu reads the live os-release and enforces the host gate.
func MustUbuntu() error {
	info, err := ReadOSRelease()
	if err != nil {
		return fmt.Errorf("read /etc/os-release: %w", err)
	}
	return CheckUbuntu(info)
}
