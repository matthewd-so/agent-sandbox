package host

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseSize parses sizes such as "512M", "4G", "4GiB" or a plain byte count.
func ParseSize(s string) (int64, error) {
	t := strings.TrimSuffix(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(s)), "IB"), "B")
	mult := int64(1)
	if n := len(t); n > 0 {
		switch t[n-1] {
		case 'K':
			mult, t = 1<<10, t[:n-1]
		case 'M':
			mult, t = 1<<20, t[:n-1]
		case 'G':
			mult, t = 1<<30, t[:n-1]
		case 'T':
			mult, t = 1<<40, t[:n-1]
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(v * float64(mult)), nil
}

// FormatSize renders bytes compactly, e.g. 4G or 512M.
func FormatSize(b int64) string {
	switch {
	case b >= 1<<30 && b%(1<<30) == 0:
		return fmt.Sprintf("%dG", b>>30)
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%dM", b>>20)
	}
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,9}$`)

// ValidID reports whether id is usable as a sandbox name. The limit of ten
// characters keeps the derived tap device name within Linux's 15 bytes.
func ValidID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid sandbox name %q: use 1-10 lowercase letters, digits or dashes", id)
	}
	return nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
