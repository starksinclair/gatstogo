package pages

import "strings"

// fallbackText returns value, or fallback when value is blank.
func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
