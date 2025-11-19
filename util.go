package main

import "strings"

// normalizeURL normalizes a relay URL by trimming whitespace, converting to lowercase, and removing trailing slashes
func normalizeURL(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "/")
	return s
}

// isValidRelayURL checks if a URL is a valid relay URL
func isValidRelayURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Must start with ws:// or wss://
	if !strings.HasPrefix(s, "ws://") && !strings.HasPrefix(s, "wss://") {
		return false
	}
	// Cannot contain query parameters or fragments
	if strings.Contains(s, "?") || strings.Contains(s, "#") {
		return false
	}
	return true
}

// isHex64 validates that a string is exactly 64 hexadecimal characters
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
