package experimental_js_test_framework

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/dop251/goja"
	"github.com/srg/blim/internal/testutils"

	_ "embed"
)

//func (suite *ConnectionTestSuite) TestSubscriptionsJS() {
//	//	js := `
//	//   let sub = t.Subscribe([{Service:"180d", Characteristics:["2a37","2a3b"]}], "EveryUpdate", 0)
//	//
//	//   t.PushMockData([
//	//       {Service:"180d", Characteristic:"2a37", Value:[0x11,0x12]},
//	//       {Service:"180d", Characteristic:"2a3b", Value:[0x21,0x22]},
//	//       {Service:"180d", Characteristic:"2a37", Value:[0x13,0x14]},
//	//       {Service:"180d", Characteristic:"2a3b", Value:[0x23,0x24]},
//	//       {Service:"180d", Characteristic:"2a37", Value:[0x15,0x16]},
//	//       {Service:"180d", Characteristic:"2a3b", Value:[0x25]}
//	//   ])
//	//
//	//   t.Sleep(200)
//	//
//	//   t.AssertJSON(
//	//       [
//	//           {"Flags":0,"Seq":1,"Values":{"2a37":["0x11","0x12"]}},
//	//           {"Flags":0,"Seq":2,"Values":{"2a3b":["0x21","0x22"]}},
//	//           {"Flags":0,"Seq":3,"Values":{"2a37":["0x13","0x14"]}},
//	//           {"Flags":0,"Seq":4,"Values":{"2a3b":["0x23","0x24"]}},
//	//           {"Flags":0,"Seq":5,"Values":{"2a37":["0x15","0x16"]}},
//	//           {"Flags":0,"Seq":6,"Values":{"2a3b":["0x25"]}}
//	//       ],
//	//       sub.ConsumeRecordsAsJSON(),
//	//       ["TsUs"]
//	//   )
//	//
//	//   sub.Cancel()
//	//`
//	//js.ExecJSScript(js, &suite.MockBLEPeripheralSuite, suite.connection)
//	js.RunJSTestsFromPath(&suite.MockBLEPeripheralSuite, suite.connection, "js-device_test")
//}

//go:embed TinyTest.js
var tinyTestJS string

// runTinyTestFile executes a JS test file using the GenericTestableDevice for BLE operations.
func runTinyTestFile(td *testutils.GenericTestableDevice, testPath string) {
	tReal := td.T()

	// Compute relative path from project root (for clickable links)
	relPath, err := filepath.Rel(".", testPath)
	if err != nil {
		relPath = testPath
	}

	// Create a fresh VM per JS test file
	vm := goja.New()
	runner := NewTestRuntime(td, vm)
	vm.Set("t", runner)

	// Load embedded TinyTest
	if _, err := vm.RunString(tinyTestJS); err != nil {
		tReal.Fatalf("failed to load embedded TinyTest: %v", err)
	}

	// Read the JS test file
	scriptBytes, err := os.ReadFile(testPath)
	if err != nil {
		tReal.Fatalf("failed to read JS test %s: %v", relPath, err)
	}

	// Compile the JS with the filename to preserve line numbers
	prog, err := goja.Compile(testPath, string(scriptBytes), false)
	if err != nil {
		tReal.Fatalf("JS compile error: file://%s:1: %s", relPath, err)
	}

	// Run the compiled program
	if _, err := vm.RunProgram(prog); err != nil {
		if exc, ok := err.(*goja.Exception); ok {
			// Replace <eval> in an exception string with relPath
			msg := exc.String()
			re := regexp.MustCompile(`<eval>:(\d+):(\d+)`)
			msg = re.ReplaceAllString(msg, relPath+":$1:$2")

			tReal.Fatalf(
				"JS runtime error: file://%s:1: %s\nJS stack:\n%s",
				relPath, exc.Error(), msg,
			)
		} else {
			tReal.Fatalf("JS runtime error in file://%s: %v", relPath, err)
		}
	}

	// Get __tests array from TinyTest
	testsVal := vm.Get("__tests")
	if testsVal == nil || len(testsVal.ToObject(vm).Keys()) == 0 {
		tReal.Fatalf("file://%s:1: defines no test() functions", relPath)
	}

	// Iterate over __tests and run each as a Go subtest
	arrObj := testsVal.ToObject(vm)

	for _, k := range arrObj.Keys() {
		itemVal := arrObj.Get(k)
		itemObj := itemVal.ToObject(vm)

		name := itemObj.Get("name").String()
		fnVal := itemObj.Get("fn")

		tReal.Run(name, func(t *testing.T) {
			fn, ok := goja.AssertFunction(fnVal)
			if !ok {
				t.Fatalf("file://%s:1: invalid JS test function %s", relPath, name)
			}

			if _, err := fn(goja.Undefined()); err != nil {
				absPath, _ := filepath.Abs(testPath)
				itemLine := itemObj.Get("line").String()

				if exc, ok := err.(*goja.Exception); ok {
					// Replace <eval> in exception with absolute path
					msg := exc.String()
					re := regexp.MustCompile(`<eval>:(\d+):(\d+)`)
					msg = re.ReplaceAllString(msg, absPath+":$1:$2")

					t.Fatalf(
						"JS test %s failed: file://%s:%s: %s\nJS stack:\n%s",
						name, relPath, itemLine, exc.Error(), exc.String(),
					)
				} else {
					t.Fatalf("JS test %s failed: file://%s:1: %v", name, absPath, err)
				}
			}
		})
	}
}

// runJSTestSuite runs all tests in a JSTestSuite using the GenericTestableDevice.
func runJSTestSuite(td *testutils.GenericTestableDevice, suite *JSTestSuite, parentPath string) {
	tReal := td.T()

	// Run top-level tests in this suite
	for _, test := range suite.Tests {
		test := test // capture loop variable
		relName := test.Name
		if parentPath != "" {
			relName = filepath.Join(parentPath, test.Name)
		}

		tReal.Run(relName, func(t *testing.T) {
			runTinyTestFile(td, test.Path)
		})
	}

	// Recurse into sub-suites
	for _, sub := range suite.Suites {
		sub := sub // capture loop variable
		subName := sub.Name
		if parentPath != "" {
			subName = filepath.Join(parentPath, sub.Name)
		}

		tReal.Run(subName, func(t *testing.T) {
			runJSTestSuite(td, &sub, subName)
		})
	}
}

// RunJSTestsFromPath discovers and runs all JS tests from the given path using GenericTestableDevice.
// The device provides access to the connection, testing.T, and fluent testing infrastructure.
func RunJSTestsFromPath(td *testutils.GenericTestableDevice, testPath string) {
	tests := discoverSuite(testPath, map[string]struct{}{
		"index.js":    {},
		"_jstypes.js": {},
	})

	runJSTestSuite(td, &tests, testPath)
}
