package lua

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// LoadScriptFromPath loads script content from a file path.
// Path resolution:
//   - Absolute paths (/foo/bar.lua) are used as-is
//   - Relative paths (./foo.lua, foo.lua) resolve from current working directory
//
// This is the SINGLE SOURCE for all script file loading in the codebase.
func LoadScriptFromPath(path string) (string, error) {
	path = filepath.Clean(path)

	var fullPath string
	if filepath.IsAbs(path) {
		fullPath = path
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		fullPath = filepath.Join(wd, path)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", fullPath, err)
	}

	return string(data), nil
}

// ResolveScript resolves a script reference to content and arguments.
// Supports:
//   - Inline Lua code: returned as-is with nil args
//   - file:// URLs: loads from file, query params become args map
//
// Example: "file://./examples/bridge.lua?format=json" returns (file content, {"format": "json"}, nil)
func ResolveScript(script string) (content string, args map[string]string, err error) {
	script = strings.TrimSpace(script)
	if !strings.HasPrefix(script, "file://") {
		return script, nil, nil
	}

	filePath, args, err := ParseFileURL(script)
	if err != nil {
		return "", nil, err
	}

	content, err = LoadScriptFromPath(filePath)
	if err != nil {
		return "", nil, err
	}

	return content, args, nil
}

// ParseFileURL parses a file:// URL and extracts the path and query parameters.
// Returns the file path and a map of query parameters.
// Example: "file://./examples/inspect.lua?format=json&verbose=true"
//
//	→ ("./examples/inspect.lua", {"format": "json", "verbose": "true"}, nil)
func ParseFileURL(fileURL string) (filePath string, params map[string]string, err error) {
	// Parse the URL
	u, err := url.Parse(fileURL)
	if err != nil {
		return "", nil, fmt.Errorf("invalid file URL: %w", err)
	}

	// Extract the path (everything after file://)
	filePath = strings.TrimPrefix(fileURL, "file://")
	if idx := strings.Index(filePath, "?"); idx != -1 {
		filePath = filePath[:idx]
	}

	// Extract query parameters
	params = make(map[string]string)
	for key, values := range u.Query() {
		if len(values) > 0 {
			params[key] = values[0] // Take first value if multiple
		}
	}

	return filePath, params, nil
}

