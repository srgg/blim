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

	go func() {
		gid := strconv.FormatUint(GetGID(), 10)
		labels := pprof.Labels("goroutine_name", name, "goroutine_id", gid)
		pprof.Do(parentCtx, labels, func(ctx context.Context) {
			ctx = context.WithValue(ctx, goroutineNameKey, name)
			fn(ctx)
		})
	}()
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

// Dump returns a formatted string listing ALL goroutines.
// Named goroutines (started with Go()) show their name in brackets.
func Dump() string {
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return "  (goroutine profile unavailable)\n"
	}

	// debug=1 for pprof labels, debug=2 for readable stacks
	var labelBuf, stackBuf bytes.Buffer
	profile.WriteTo(&labelBuf, 1)
	profile.WriteTo(&stackBuf, 2)

	// Parse labels: # labels: {"goroutine_name":"x","goroutine_id":"123"}
	names := make(map[string]string) // gid -> name
	for _, line := range bytes.Split(labelBuf.Bytes(), []byte("\n")) {
		name := extractJSONValue(line, `"goroutine_name":"`)
		gid := extractJSONValue(line, `"goroutine_id":"`)
		if gid != "" && name != "" {
			names[gid] = name
		}
	}

	// Format output
	var result bytes.Buffer
	var header, frame []byte
	var gid string

	flush := func() {
		if len(header) == 0 {
			return
		}
		result.WriteString("  ")
		result.Write(header)
		if name := names[gid]; name != "" {
			result.WriteString(" [NAME: " + name + "]")
		}
		result.WriteByte('\n')
		if len(frame) > 0 {
			result.WriteString("    → ")
			result.Write(frame)
			result.WriteByte('\n')
		}
	}

	for _, line := range bytes.Split(stackBuf.Bytes(), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("goroutine ")) {
			flush()
			header, frame = line, nil
			// Extract GID from "goroutine 123 [state]:"
			if idx := bytes.IndexByte(line[10:], ' '); idx > 0 {
				gid = string(line[10 : 10+idx])
			}
		} else if len(header) > 0 && frame == nil && len(line) > 0 && line[0] != '\t' {
			frame = bytes.TrimSpace(line)
		}
	}
	flush()

	if result.Len() == 0 {
		return "  (no goroutines found)\n"
	}
	return result.String()
}

// extractJSONValue extracts value after key like `"key":"` returning content until next `"`
func extractJSONValue(line []byte, key string) string {
	keyBytes := []byte(key)
	if start := bytes.Index(line, keyBytes); start >= 0 {
		start += len(keyBytes)
		if end := bytes.IndexByte(line[start:], '"'); end > 0 {
			return string(line[start : start+end])
		}
	}
	return ""
}
