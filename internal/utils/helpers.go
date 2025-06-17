package utils

// MakeMap creates and returns a map[string]string containing a single key-value pair.
func MakeMap(key, value string) map[string]string {
	return map[string]string{key: value}
}
