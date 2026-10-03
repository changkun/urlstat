// Package envutil provides helpers for reading typed values from
// environment variables with defaults and optional minimum bounds.
package envutil

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Int reads an integer from environment variable key.
// Returns defaultVal if absent, empty, or unparseable.
func Int(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// IntMin reads an integer from environment variable key with a minimum bound.
// Returns defaultVal if absent, empty, unparseable, or below min.
func IntMin(key string, defaultVal, minVal int) int {
	n := Int(key, defaultVal)
	if n < minVal {
		return defaultVal
	}
	return n
}

// Duration reads a time.Duration from environment variable key.
// Returns defaultVal if absent, empty, or unparseable.
func Duration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// IsTruthy reports whether v is one of the affirmative spellings a boolean
// environment variable is conventionally set to: "1", "true", "yes", or "on",
// in any case and with surrounding space ignored. Everything else, the empty
// string included, is false, so an unset variable reads as off.
func IsTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
