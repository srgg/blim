package experimental_js_test_framework

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// JSTest represents a single JS test file
type JSTest struct {
	Name string // human-readable test name
	Path string // full path to the .js file
}

// JSTestSuite represents a JS test suite (folder)
type JSTestSuite struct {
	Name   string        // suite name
	Path   string        // folder path
	Tests  []JSTest      // JS device_test in this folder
	Suites []JSTestSuite // nested sub-suites
}

// exists checks if a file exists
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// parseSuiteName reads index.js (or any JS file) and looks for @suite "Name"
func parseSuiteName(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "//") && strings.Contains(line, "@suite") {
			parts := strings.SplitN(line, "@suite", 2)
			if len(parts) == 2 {
				return strings.Trim(parts[1], ` "'`)
			}
		}
		if strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "/**") {
			// multi-line JSDoc
			var jsdocLines []string
			jsdocLines = append(jsdocLines, line)
			for scanner.Scan() {
				l := strings.TrimSpace(scanner.Text())
				jsdocLines = append(jsdocLines, l)
				if strings.Contains(l, "*/") {
					break
				}
			}
			for _, l := range jsdocLines {
				if strings.Contains(l, "@suite") {
					parts := strings.SplitN(l, "@suite", 2)
					if len(parts) == 2 {
						return strings.Trim(parts[1], ` "'`)
					}
				}
			}
		}
	}
	return ""
}

// parseTestName reads a JS file and returns the @test tag if present
func parseTestName(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "//") && strings.Contains(line, "@test") {
			parts := strings.SplitN(line, "@test", 2)
			if len(parts) == 2 {
				return strings.Trim(parts[1], ` "'`)
			}
		}
		if strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "/**") {
			// multi-line JSDoc
			var jsdocLines []string
			jsdocLines = append(jsdocLines, line)
			for scanner.Scan() {
				l := strings.TrimSpace(scanner.Text())
				jsdocLines = append(jsdocLines, l)
				if strings.Contains(l, "*/") {
					break
				}
			}
			for _, l := range jsdocLines {
				if strings.Contains(l, "@test") {
					parts := strings.SplitN(l, "@test", 2)
					if len(parts) == 2 {
						return strings.Trim(parts[1], ` "'`)
					}
				}
			}
		}
	}
	return ""
}

// discoverSuite recursively scans a directory and builds JSTestSuite structure
func discoverSuite(dirPath string, ignoreFiles map[string]struct{}) JSTestSuite {
	suiteName := filepath.Base(dirPath)

	// Check for index.js @suite
	indexFile := filepath.Join(dirPath, "index.js")
	if exists(indexFile) {
		if name := parseSuiteName(indexFile); name != "" {
			suiteName = name
		}
	}

	var tests []JSTest
	var subSuites []JSTestSuite

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return JSTestSuite{} // skip unreadable dirs
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			sub := discoverSuite(fullPath, ignoreFiles)
			if len(sub.Tests) > 0 || len(sub.Suites) > 0 {
				subSuites = append(subSuites, sub)
			}
			//} else if filepath.Ext(entry.Name()) == ".js" && entry.Name() != "index.js" {
		} else if filepath.Ext(entry.Name()) == ".js" {
			if _, ok := ignoreFiles[entry.Name()]; !ok {
				testName := parseTestName(fullPath)
				if testName == "" {
					testName = strings.TrimSuffix(entry.Name(), ".js")
				}
				tests = append(tests, JSTest{
					Name: testName,
					Path: fullPath,
				})
			}
		}
	}

	// Skip empty suites
	if len(tests) == 0 && len(subSuites) == 0 {
		return JSTestSuite{}
	}

	return JSTestSuite{
		Name:   suiteName,
		Path:   dirPath,
		Tests:  tests,
		Suites: subSuites,
	}
}
