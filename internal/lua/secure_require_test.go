//go:build test

package lua

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecureModuleLoader_PathTraversal(t *testing.T) {
	// GOAL: Verify path traversal attacks are blocked by SecureModuleLoader
	//
	// TEST SCENARIO: Attempt path traversal patterns → error returned → module not loaded

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	loader := NewSecureModuleLoader(logger)

	tmpDir := t.TempDir()
	dummyScript := filepath.Join(tmpDir, "script.lua")
	require.NoError(t, os.WriteFile(dummyScript, []byte(""), 0644))
	require.NoError(t, loader.AddPaths(dummyScript, nil))

	tests := []struct {
		name       string
		moduleName string
		wantErr    string
	}{
		{
			name:       "path traversal with ..",
			moduleName: "../etc/passwd",
			wantErr:    "path traversal not allowed",
		},
		{
			name:       "path traversal embedded in module",
			moduleName: "foo/../bar",
			wantErr:    "path traversal not allowed",
		},
		{
			name:       "absolute path",
			moduleName: "/etc/passwd",
			wantErr:    "absolute paths not allowed",
		},
		{
			name:       "module not found",
			moduleName: "nonexistent_module",
			wantErr:    "module not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := loader.resolveModule(tt.moduleName)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSecureModuleLoader_ValidModuleResolution(t *testing.T) {
	// GOAL: Verify valid modules are resolved correctly from allowed paths
	//
	// TEST SCENARIO: Create module in allowed dir → resolve by name → content returned

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	// Create temp directory with a test module
	tmpDir := t.TempDir()
	testModulePath := filepath.Join(tmpDir, "testmodule.lua")
	testModuleContent := `return { name = "test" }`
	require.NoError(t, os.WriteFile(testModulePath, []byte(testModuleContent), 0644))

	// Use scriptPath mechanism to add directory (bypasses blocklist)
	dummyScript := filepath.Join(tmpDir, "script.lua")
	require.NoError(t, os.WriteFile(dummyScript, []byte(""), 0644))

	loader := NewSecureModuleLoader(logger)
	require.NoError(t, loader.AddPaths(dummyScript, nil))

	// Test module resolution - path may be resolved (e.g., /var → /private/var on macOS)
	fullPath, content, err := loader.resolveModule("testmodule")
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(fullPath, "testmodule.lua"))
	assert.Equal(t, testModuleContent, content)
}

func TestSecureModuleLoader_DotNotation(t *testing.T) {
	// GOAL: Verify Lua dot notation converts to filesystem paths correctly
	//
	// TEST SCENARIO: Request "utils.helpers" → resolved to utils/helpers.lua → content returned

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	// Create nested directory structure
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "utils")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))

	testModulePath := filepath.Join(nestedDir, "helpers.lua")
	testModuleContent := `return { helper = true }`
	require.NoError(t, os.WriteFile(testModulePath, []byte(testModuleContent), 0644))

	// Use scriptPath mechanism to add directory (bypasses blocklist)
	dummyScript := filepath.Join(tmpDir, "script.lua")
	require.NoError(t, os.WriteFile(dummyScript, []byte(""), 0644))

	loader := NewSecureModuleLoader(logger)
	require.NoError(t, loader.AddPaths(dummyScript, nil))

	// Test dot notation resolution (utils.helpers → utils/helpers.lua)
	fullPath, content, err := loader.resolveModule("utils.helpers")
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(fullPath, "utils/helpers.lua"))
	assert.Equal(t, testModuleContent, content)
}

func TestSecureModuleLoader_InitLua(t *testing.T) {
	// GOAL: Verify init.lua is used when requiring a directory module
	//
	// TEST SCENARIO: Request "mymodule" with mymodule/init.lua present → init.lua loaded

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	// Create directory with init.lua
	tmpDir := t.TempDir()
	moduleDir := filepath.Join(tmpDir, "mymodule")
	require.NoError(t, os.MkdirAll(moduleDir, 0755))

	initPath := filepath.Join(moduleDir, "init.lua")
	initContent := `return { initialized = true }`
	require.NoError(t, os.WriteFile(initPath, []byte(initContent), 0644))

	// Use scriptPath mechanism to add directory (bypasses blocklist)
	dummyScript := filepath.Join(tmpDir, "script.lua")
	require.NoError(t, os.WriteFile(dummyScript, []byte(""), 0644))

	loader := NewSecureModuleLoader(logger)
	require.NoError(t, loader.AddPaths(dummyScript, nil))

	// Test init.lua resolution (mymodule → mymodule/init.lua)
	fullPath, content, err := loader.resolveModule("mymodule")
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(fullPath, "mymodule/init.lua"))
	assert.Equal(t, initContent, content)
}

func TestValidateLibPath_BlockedPaths(t *testing.T) {
	// GOAL: Verify system directories are blocked from --lua-lib flag
	//
	// TEST SCENARIO: Attempt to add system path → error with "system directory is not allowed"

	blockedPaths := []struct {
		name string
		path string
	}{
		{name: "/etc is blocked", path: "/etc"},
		{name: "/etc/subdir is blocked", path: "/etc/ssl"},
		{name: "/var is blocked", path: "/var"},
		{name: "/usr is blocked", path: "/usr"},
		{name: "/System is blocked", path: "/System"},
		{name: "/Library is blocked", path: "/Library"},
		{name: "/private is blocked", path: "/private"},
	}

	for _, tt := range blockedPaths {
		t.Run(tt.name, func(t *testing.T) {
			// Skip if path doesn't exist (e.g., /System on Linux)
			if _, err := os.Stat(tt.path); os.IsNotExist(err) {
				t.Skipf("path %s does not exist on this system", tt.path)
			}

			_, err := validateLibPath(tt.path)
			require.Error(t, err, "system path MUST be rejected")
			assert.Contains(t, err.Error(), "system directory is not allowed")
		})
	}
}

func TestValidateLibPath_NonexistentPath(t *testing.T) {
	// GOAL: Verify nonexistent paths are rejected by validateLibPath
	//
	// TEST SCENARIO: Validate nonexistent path → error with "directory does not exist"

	_, err := validateLibPath("/nonexistent/path/that/does/not/exist")
	require.Error(t, err, "nonexistent path MUST be rejected")
	assert.Contains(t, err.Error(), "directory does not exist")
}

func TestValidateLibPath_FileNotDirectory(t *testing.T) {
	// GOAL: Verify regular files are rejected (only directories allowed)
	//
	// TEST SCENARIO: Validate file path → error with "path is not a directory"

	tmpDir := t.TempDir()
	tmpFileName := filepath.Join(tmpDir, "testfile")
	require.NoError(t, os.WriteFile(tmpFileName, []byte(""), 0644))

	_, err := validateLibPath(tmpFileName)
	require.Error(t, err, "file path MUST be rejected")
	assert.Contains(t, err.Error(), "path is not a directory")
}

func TestSecureModuleLoader_NoPathsConfigured(t *testing.T) {
	// GOAL: Verify module resolution fails when no paths are configured
	//
	// TEST SCENARIO: Create loader without paths → resolve module → "no search paths configured" error

	logger := logrus.New()
	loader := NewSecureModuleLoader(logger)

	_, _, err := loader.resolveModule("anymodule")
	require.Error(t, err, "resolution MUST fail without configured paths")
	assert.Contains(t, err.Error(), "no search paths configured")
}

func TestSecureModuleLoader_SymlinkOutsideAllowed(t *testing.T) {
	// GOAL: Verify symlinks pointing outside allowed paths are rejected
	//
	// TEST SCENARIO: Create symlink to external dir → resolve module → rejected as not found

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	allowedDir := t.TempDir()
	targetDir := t.TempDir()

	targetModule := filepath.Join(targetDir, "secret.lua")
	require.NoError(t, os.WriteFile(targetModule, []byte("return 'secret'"), 0644))

	symlinkPath := filepath.Join(allowedDir, "secret.lua")
	require.NoError(t, os.Symlink(targetModule, symlinkPath))

	dummyScript := filepath.Join(allowedDir, "script.lua")
	require.NoError(t, os.WriteFile(dummyScript, []byte(""), 0644))

	loader := NewSecureModuleLoader(logger)
	require.NoError(t, loader.AddPaths(dummyScript, nil))

	_, _, err := loader.resolveModule("secret")
	require.Error(t, err, "symlink outside allowed paths MUST be rejected")
	assert.Contains(t, err.Error(), "module not found")
}

func TestSecureModuleLoader_ScriptPathAddsDirectory(t *testing.T) {
	// GOAL: Verify script's directory is automatically added to allowed paths
	//
	// TEST SCENARIO: Add script path → resolve sibling module → found successfully

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "myscript.lua")
	require.NoError(t, os.WriteFile(scriptPath, []byte("require('mylib')"), 0644))

	libPath := filepath.Join(tmpDir, "mylib.lua")
	libContent := `return { loaded = true }`
	require.NoError(t, os.WriteFile(libPath, []byte(libContent), 0644))

	loader := NewSecureModuleLoader(logger)
	require.NoError(t, loader.AddPaths(scriptPath, nil))

	fullPath, content, err := loader.resolveModule("mylib")
	require.NoError(t, err, "module in script directory MUST be resolved")
	assert.True(t, strings.HasSuffix(fullPath, "mylib.lua"), "path MUST end with mylib.lua")
	assert.Equal(t, libContent, content, "content MUST match")
}

func TestSecureModuleLoader_PreloadedModules(t *testing.T) {
	// GOAL: Verify LuaJIT preloaded modules (ffi, bit, jit) are accessible via secure require
	//
	// TEST SCENARIO: Create LuaEngine → require('ffi') → module loads successfully

	tests := []struct {
		name       string
		moduleName string
	}{
		{name: "ffi module", moduleName: "ffi"},
		{name: "bit module", moduleName: "bit"},
		{name: "jit module", moduleName: "jit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewLuaEngine(logrus.New())
			defer engine.Close()

			script := `local mod = require('` + tt.moduleName + `'); print("loaded: " .. type(mod))`
			err := engine.ExecuteScript(t.Context(), script)
			require.NoError(t, err, "preloaded module %s MUST be accessible", tt.moduleName)
		})
	}
}

func TestSecureModuleLoader_SyntaxErrorInModule(t *testing.T) {
	// GOAL: Verify syntax errors in required modules propagate to caller
	//
	// TEST SCENARIO: Require module with syntax error → error returned → contains "syntax error"

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	tmpDir := t.TempDir()

	// Create a module with invalid Lua syntax
	badModulePath := filepath.Join(tmpDir, "bad_module.lua")
	badModuleContent := `WHat is that on line 1
return { name = "test" }`
	require.NoError(t, os.WriteFile(badModulePath, []byte(badModuleContent), 0644))

	// Create main script that requires the bad module
	mainScriptPath := filepath.Join(tmpDir, "main.lua")
	require.NoError(t, os.WriteFile(mainScriptPath, []byte(""), 0644))

	engine := NewLuaEngine(logger)
	defer engine.Close()

	// Configure paths so require can find the bad module
	require.NoError(t, engine.SetPackagePaths(mainScriptPath, nil))

	// Execute script that requires the bad module
	script := `local mod = require('bad_module')`
	err := engine.ExecuteScript(t.Context(), script)

	// MUST return error containing syntax error information
	require.Error(t, err, "require of module with syntax error MUST fail")
	assert.Contains(t, err.Error(), "syntax error", "error MUST mention syntax error")
}

func TestSecureModuleLoader_RuntimeErrorInModule(t *testing.T) {
	// GOAL: Verify runtime errors in required modules propagate to caller
	//
	// TEST SCENARIO: Require module with runtime error → error returned → contains error message

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	tmpDir := t.TempDir()

	// Create a module that has valid syntax but throws runtime error
	badModulePath := filepath.Join(tmpDir, "runtime_error_module.lua")
	badModuleContent := `error("intentional runtime error in module")`
	require.NoError(t, os.WriteFile(badModulePath, []byte(badModuleContent), 0644))

	// Create main script
	mainScriptPath := filepath.Join(tmpDir, "main.lua")
	require.NoError(t, os.WriteFile(mainScriptPath, []byte(""), 0644))

	engine := NewLuaEngine(logger)
	defer engine.Close()

	require.NoError(t, engine.SetPackagePaths(mainScriptPath, nil))

	script := `local mod = require('runtime_error_module')`
	err := engine.ExecuteScript(t.Context(), script)

	require.Error(t, err, "require of module with runtime error MUST fail")
	assert.Contains(t, err.Error(), "intentional runtime error", "error MUST contain the error message")
}
