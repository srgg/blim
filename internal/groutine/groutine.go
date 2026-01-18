package groutine

import (
	"bytes"
	"context"
	"runtime"
	"runtime/pprof"
	"strconv"
)

type ctxKey string

const goroutineNameKey ctxKey = "goroutine_name"

// Go starts a goroutine with a name, optional parent context
// Example usage:
//
//	groutine.Go(ctx, "worker-42", func(ctx context.Context) {
//	    // work
//	})
//
// If parentCtx is nil, context.Background() is used.
func Go(parentCtx context.Context, name string, fn func(ctx context.Context)) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	labels := pprof.Labels("goroutine_name", name)

	go pprof.Do(parentCtx, labels, func(ctx context.Context) {
		ctx = context.WithValue(ctx, goroutineNameKey, name)
		fn(ctx)
	})
}

// GetName retrieves the goroutine name from the context.
func GetName(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(goroutineNameKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetGID returns the numeric goroutine ID (hacky, for debugging).
func GetGID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	if i < 0 {
		return 0
	}
	gid, _ := strconv.ParseUint(string(b[:i]), 10, 64)
	return gid
}

// IsRunning checks if a goroutine with the given name is currently running.
// Uses pprof labels set by Go() to identify goroutines.
// Matches exact label pattern to avoid substring false positives.
func IsRunning(name string) bool {
	var buf bytes.Buffer
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return false
	}
	_ = profile.WriteTo(&buf, 1) // debug=1 includes labels

	// Match exact label pattern: goroutine_name="name" (with quotes)
	// This prevents "sub" from matching "sub-1" or "subscription"
	pattern := []byte(`goroutine_name="` + name + `"`)
	return bytes.Contains(buf.Bytes(), pattern)
}

// Dump returns a formatted string listing all goroutines with pprof labels.
// Shows goroutine ID and name for each labeled goroutine.
func Dump() string {
	var buf bytes.Buffer
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return "  (goroutine profile unavailable)\n"
	}

	// Write debug=1 format which includes labels
	if err := profile.WriteTo(&buf, 1); err != nil {
		return "  (error dumping goroutines: " + err.Error() + ")\n"
	}

	// Parse and format the output, highlighting named goroutines
	lines := bytes.Split(buf.Bytes(), []byte("\n"))
	var result bytes.Buffer
	var currentGoroutine []byte
	var hasLabel bool

	for _, line := range lines {
		// Detect goroutine header
		if bytes.HasPrefix(line, []byte("goroutine ")) {
			// Flush previous goroutine if it had a label
			if len(currentGoroutine) > 0 && hasLabel {
				result.WriteString("  ")
				result.Write(currentGoroutine)
				result.WriteString("\n")
			}
			currentGoroutine = line
			hasLabel = false
		} else if bytes.Contains(line, []byte(`goroutine_name="`)) {
			// This goroutine has our pprof label - extract and highlight
			hasLabel = true
			// Extract the name
			start := bytes.Index(line, []byte(`goroutine_name="`)) + len(`goroutine_name="`)
			end := bytes.Index(line[start:], []byte(`"`))
			if end > 0 {
				name := line[start : start+end]
				currentGoroutine = append(currentGoroutine, []byte(" [NAME: ")...)
				currentGoroutine = append(currentGoroutine, name...)
				currentGoroutine = append(currentGoroutine, ']')
			}
		}
	}

	// Flush last goroutine if it had a label
	if len(currentGoroutine) > 0 && hasLabel {
		result.WriteString("  ")
		result.Write(currentGoroutine)
		result.WriteString("\n")
	}

	if result.Len() == 0 {
		return "  (no named goroutines found)\n"
	}

	return result.String()
}
