package units

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBytes parses values like "50G", "4096M", "512K", or a raw byte count.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "K"):
		mult = 1024
		upper = strings.TrimSuffix(upper, "K")
	case strings.HasSuffix(upper, "M"):
		mult = 1024 * 1024
		upper = strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "G"):
		mult = 1024 * 1024 * 1024
		upper = strings.TrimSuffix(upper, "G")
	case strings.HasSuffix(upper, "T"):
		mult = 1024 * 1024 * 1024 * 1024
		upper = strings.TrimSuffix(upper, "T")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(upper), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must be non-negative")
	}
	return n * mult, nil
}

// ParseMB parses a memory size into mebibytes.
func ParseMB(s string) (int, error) {
	b, err := ParseBytes(s)
	if err != nil {
		return 0, err
	}
	return int(b / (1024 * 1024)), nil
}

// ParseGB parses a disk size into gibibytes, rounding up.
func ParseGB(s string) (int, error) {
	b, err := ParseBytes(s)
	if err != nil {
		return 0, err
	}
	gb := b / (1024 * 1024 * 1024)
	if b%(1024*1024*1024) != 0 {
		gb++
	}
	return int(gb), nil
}

// FormatBytes renders a byte count as a compact human string.
func FormatBytes(n int64) string {
	switch {
	case n >= 1024*1024*1024 && n%(1024*1024*1024) == 0:
		return fmt.Sprintf("%dG", n/(1024*1024*1024))
	case n >= 1024*1024 && n%(1024*1024) == 0:
		return fmt.Sprintf("%dM", n/(1024*1024))
	case n >= 1024 && n%1024 == 0:
		return fmt.Sprintf("%dK", n/1024)
	default:
		return strconv.FormatInt(n, 10)
	}
}
