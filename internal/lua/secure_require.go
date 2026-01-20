package lua

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aarzilli/golua/lua"
	"github.com/sirupsen/logrus"
)

// SecureModuleLoader enforces path restrictions on Lua require() calls.
// Singleton: created once in LuaEngine.resetInternal(), modified via AddPaths().
type SecureModuleLoader struct {
	allowedPaths []string // Absolute paths of allowed directories (initially empty)
	logger       *logrus.Logger
}

// systemBlocklist contains directories that cannot be used with --lua-lib.
var systemBlocklist = []string{
	"/etc",
	"/var",
	"/usr",
	"/bin",
	"/sbin",
	"/System",       // macOS
	"/Library",      // macOS
	"/Applications", // macOS
	"/private",      // macOS
}

// NewSecureModuleLoader creates a loader with empty allowedPaths.
func NewSecureModuleLoader(logger *logrus.Logger) *SecureModuleLoader {
	return &SecureModuleLoader{
		allowedPaths: []string{},
		logger:       logger,
	}
}

// AddPaths adds script directory and validated lib paths to allowedPaths.
func (s *SecureModuleLoader) AddPaths(scriptPath string, libPaths []string) error {
	// 1. If scriptPath provided, add its directory to allowedPaths
	if scriptPath != "" {
		scriptDir, err := resolveScriptPath(scriptPath)
		if err != nil {
			return fmt.Errorf("invalid script path %q: %w", scriptPath, err)
		}
		s.allowedPaths = append(s.allowedPaths, scriptDir)
		s.logger.WithField("path", scriptDir).Debug("Added script directory to allowed paths")
	}

	// 2. For each libPath: validate and get resolved real path
	for _, libPath := range libPaths {
		realPath, err := validateLibPath(libPath)
		if err != nil {
			return fmt.Errorf("invalid --lua-lib path %q: %w", libPath, err)
		}
		s.allowedPaths = append(s.allowedPaths, realPath)
		s.logger.WithField("path", realPath).Debug("Added --lua-lib path to allowed paths")
	}

	return nil
}

// resolveScriptPath resolves a script path to its canonical directory.
// Handles symlinks: if script is a symlink, returns the directory of the target.
// No blocklist validation - script's own directory is implicitly trusted.
func resolveScriptPath(scriptPath string) (string, error) {
	absPath, err := filepath.Abs(scriptPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}
	// Resolve symlinks on the script path itself (handles symlink-to-file)
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve symlinks: %w", err)
	}
	return filepath.Dir(realPath), nil
}

// validateLibPath checks if a path is safe for --lua-lib (unexported - internal use only).
// Returns the resolved real path if valid.
func validateLibPath(path string) (string, error) {
	// Resolve to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}

	// Check if path exists and is a directory
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("directory does not exist")
		}
		return "", fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}

	// Resolve symlinks to get canonical path (e.g., /var → /private/var on macOS)
	// This ensures blocklist check works correctly on systems with symlinked directories
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve symlinks: %w", err)
	}

	// Check against system blocklist using the REAL path
	for _, blocked := range systemBlocklist {
		if realPath == blocked || strings.HasPrefix(realPath, blocked+"/") {
			return "", fmt.Errorf("system directory is not allowed")
		}
	}

	return realPath, nil
}

// resolveModule finds module FILE in allowed paths (pure Go, no Lua state).
// Does NOT handle preloaded modules - that's done in Register's inner function.
func (s *SecureModuleLoader) resolveModule(moduleName string) (fullPath, content string, err error) {
	// 1. Validate module name
	if strings.Contains(moduleName, "..") {
		return "", "", fmt.Errorf("path traversal not allowed")
	}
	if strings.HasPrefix(moduleName, "/") {
		return "", "", fmt.Errorf("absolute paths not allowed in module name")
	}

	// 2. Check if filesystem paths are configured
	if len(s.allowedPaths) == 0 {
		return "", "", fmt.Errorf("no search paths configured (use --lua-lib or run from script file)")
	}

	// 3. Search allowed paths for module file
	// "mylib" → "mylib.lua" or "mylib/init.lua"
	// "utils.helpers" → "utils/helpers.lua" or "utils/helpers/init.lua"
	patterns := []string{
		strings.ReplaceAll(moduleName, ".", "/") + ".lua",
		strings.ReplaceAll(moduleName, ".", "/") + "/init.lua",
	}

	for _, allowedDir := range s.allowedPaths {
		for _, pattern := range patterns {
			candidate := filepath.Join(allowedDir, pattern)

			// Resolve symlinks to get real path
			realPath, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				continue // File doesn't exist or can't resolve
			}

			// SECURITY: Verify resolved path is still under an allowed directory
			isAllowed := false
			for _, allowed := range s.allowedPaths {
				if strings.HasPrefix(realPath, allowed+"/") || realPath == allowed {
					isAllowed = true
					break
				}
			}
			if !isAllowed {
				s.logger.Warnf("Module %q symlink outside allowed paths: %s", moduleName, realPath)
				continue
			}

			// Read file content
			data, err := os.ReadFile(realPath)
			if err != nil {
				continue
			}

			return realPath, string(data), nil
		}
	}

	return "", "", fmt.Errorf("module not found in allowed paths")
}

// Register registers custom require() in Lua VM.
// Called from LuaEngine.resetInternal() which already holds mutex.
func (s *SecureModuleLoader) Register(engine *LuaEngine) {
	engine.doWithStateInternal(func(L *lua.State) interface{} {
		// Create the secure require function
		requireFn := engine.SafeWrapGoFunction("require", func(L *lua.State) int {
			moduleName := L.ToString(1)

			// STEP 1: Check package.loaded (already loaded modules)
			L.GetGlobal("package")
			L.GetField(-1, "loaded")
			L.GetField(-1, moduleName)
			if !L.IsNil(-1) {
				// Module already loaded, return it (leave it on stack)
				return 1
			}
			L.Pop(3) // Pop nil, loaded, package

			// STEP 2: Check package.preload (LuaJIT built-ins like ffi, bit, jit)
			L.GetGlobal("package")
			L.GetField(-1, "preload")
			L.GetField(-1, moduleName)
			if L.IsFunction(-1) {
				// Call the preload function with moduleName as argument
				L.PushString(moduleName)
				L.Call(1, 1) // 1 arg, 1 result

				// Cache in package.loaded
				L.GetGlobal("package")
				L.GetField(-1, "loaded")
				L.PushValue(-3) // Copy module result
				L.SetField(-2, moduleName)
				L.Pop(2) // Pop loaded, package

				// Clean up preload lookup stack (package, preload still on stack)
				// Stack: [result, preload, package] -> need to return just result
				L.Remove(-2) // Remove preload
				L.Remove(-2) // Remove package
				return 1
			}
			L.Pop(3) // Pop nil/non-function, preload, package

			// STEP 3: Resolve from filesystem via SecureModuleLoader
			fullPath, content, err := s.resolveModule(moduleName)
			if err != nil {
				L.RaiseError(fmt.Sprintf("require('%s'): %s", moduleName, err.Error()))
				return 0
			}

			// STEP 4: Load the module source
			if loadErr := L.LoadString(content); loadErr != 0 {
				errMsg := L.ToString(-1)
				L.Pop(1)
				L.RaiseError(fmt.Sprintf("require('%s'): syntax error: %s", moduleName, errMsg))
				return 0
			}

			// STEP 5: Execute with (moduleName, fullPath) as arguments (Lua convention)
			L.PushString(moduleName)
			L.PushString(fullPath)
			if err := L.Call(2, 1); err != nil {
				L.RaiseError(fmt.Sprintf("require('%s'): %s", moduleName, err.Error()))
				return 0
			}

			// STEP 6: Cache in package.loaded
			L.GetGlobal("package")
			L.GetField(-1, "loaded")
			L.PushValue(-3) // Copy module result
			L.SetField(-2, moduleName)
			L.Pop(2) // Pop loaded, package

			return 1 // Return the module result
		})

		L.PushGoFunction(requireFn)
		L.SetGlobal("require")
		return nil
	})
}
