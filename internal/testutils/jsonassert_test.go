package testutils

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// JSONAsserterTestSuite provides comprehensive device_test for JSONAsserter functionality
type JSONAsserterTestSuite struct {
	suite.Suite
}

func (s *JSONAsserterTestSuite) TestDefaultOptions() {
	// GOAL: Verify default options are set correctly
	//
	// TEST SCENARIO: Create new JSONAsserter → default options applied → values match expected defaults

	ja := NewJSONAsserter(s.T())
	opts := ja.GetOptions()

	s.True(opts.IgnoreExtraKeys, "IgnoreExtraKeys should default to true")
	s.True(opts.NilToEmptyArray, "NilToEmptyArray should default to true")
	s.True(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should default to true")
	s.False(opts.CompareOnlyExpectedKeys, "CompareOnlyExpectedKeys should default to false")
	s.Empty(opts.IgnoredFields, "IgnoredFields should default to empty slice")
}

func (s *JSONAsserterTestSuite) TestFunctionalOptions() {
	s.Run("WithAllowPresencePlaceholder false", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(false),
		)
		opts := ja.GetOptions()

		s.False(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should be false when explicitly set")
		s.True(opts.IgnoreExtraKeys, "IgnoreExtraKeys should remain true from defaults")
		s.True(opts.NilToEmptyArray, "NilToEmptyArray should remain true from defaults")
	})

	s.Run("WithIgnoreExtraKeys false", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreExtraKeys(false),
		)
		opts := ja.GetOptions()

		s.False(opts.IgnoreExtraKeys, "IgnoreExtraKeys should be false when explicitly set")
		s.True(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should remain true from defaults")
		s.True(opts.NilToEmptyArray, "NilToEmptyArray should remain true from defaults")
	})

	s.Run("WithNilToEmptyArray false", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithNilToEmptyArray(false),
		)
		opts := ja.GetOptions()

		s.False(opts.NilToEmptyArray, "NilToEmptyArray should be false when explicitly set")
		s.True(opts.IgnoreExtraKeys, "IgnoreExtraKeys should remain true from defaults")
		s.True(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should remain true from defaults")
	})

	s.Run("WithCompareOnlyExpectedKeys true", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
		)
		opts := ja.GetOptions()

		s.True(opts.CompareOnlyExpectedKeys, "CompareOnlyExpectedKeys should be true when explicitly set")
		s.True(opts.IgnoreExtraKeys, "IgnoreExtraKeys should remain true from defaults")
		s.True(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should remain true from defaults")
		s.True(opts.NilToEmptyArray, "NilToEmptyArray should remain true from defaults")
	})

	s.Run("Multiple options", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(false),
			WithIgnoreExtraKeys(false),
			WithNilToEmptyArray(true), // explicitly set to true
			WithCompareOnlyExpectedKeys(true),
		)
		opts := ja.GetOptions()

		s.False(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should be false")
		s.False(opts.IgnoreExtraKeys, "IgnoreExtraKeys should be false")
		s.True(opts.NilToEmptyArray, "NilToEmptyArray should be true")
		s.True(opts.CompareOnlyExpectedKeys, "CompareOnlyExpectedKeys should be true")
	})
}

func (s *JSONAsserterTestSuite) TestLegacyStructOptions() {
	// GOAL: Verify legacy struct-based options configuration works
	//
	// TEST SCENARIO: Configure options via struct → options applied correctly → values match expected

	ja := NewJSONAsserter(s.T()).WithOptionsStruct(JSONAssertOptions{
		AllowPresencePlaceholder: true,
		NilToEmptyArray:          true,
		IgnoreExtraKeys:          false, // override default
	})
	opts := ja.GetOptions()

	s.True(opts.AllowPresencePlaceholder, "AllowPresencePlaceholder should be true")
	s.True(opts.NilToEmptyArray, "NilToEmptyArray should be true")
	s.False(opts.IgnoreExtraKeys, "IgnoreExtraKeys should be false when explicitly set")
}

func (s *JSONAsserterTestSuite) TestPresencePlaceholder() {
	s.Run("allows presence placeholder when enabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(true),
		)

		actualJSON := `{"id": "123", "timestamp": 1758348286}`
		expectedJSON := `{"id": "123", "timestamp": "<<PRESENCE>>"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with presence placeholder enabled")
	})

	s.Run("rejects presence placeholder when disabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(false),
		)

		actualJSON := `{"id": "123", "timestamp": 1758348286}`
		expectedJSON := `{"id": "123", "timestamp": "<<PRESENCE>>"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with presence placeholder disabled")
		s.Contains(diff, "<<PRESENCE>>", "Expected diff to contain <<PRESENCE>>")
	})
}

func (s *JSONAsserterTestSuite) TestIgnoreExtraKeys() {
	s.Run("ignores extra keys when enabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreExtraKeys(true),
		)

		actualJSON := `{"id": "123", "name": "test", "extra": "value"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with IgnoreExtraKeys enabled")
	})

	s.Run("detects extra keys when disabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreExtraKeys(false),
		)

		actualJSON := `{"id": "123", "name": "test", "extra": "value"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with IgnoreExtraKeys disabled")
	})
}

func (s *JSONAsserterTestSuite) TestComplexScenarios() {
	s.Run("complex object with all features", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(true),
			WithIgnoreExtraKeys(true),
			WithNilToEmptyArray(true),
		)

		actualJSON := `{
			"id": "device123",
			"name": "Test Device",
			"timestamp": 1758348286,
			"services": null,
			"extra_field": "should_be_ignored"
		}`

		expectedJSON := `{
			"id": "device123",
			"name": "Test Device",
			"timestamp": "<<PRESENCE>>",
			"services": []
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with all features enabled")
	})

	s.Run("strict comparison with all features disabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithAllowPresencePlaceholder(false),
			WithIgnoreExtraKeys(false),
			WithNilToEmptyArray(false),
		)

		actualJSON := `{
			"id": "device123",
			"name": "Test Device",
			"timestamp": 1758348286,
			"services": null,
			"extra_field": "present"
		}`

		expectedJSON := `{
			"id": "device123",
			"name": "Test Device",
			"timestamp": "<<PRESENCE>>",
			"services": []
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with all features disabled")
		s.Contains(diff, "<<PRESENCE>>", "Expected diff to contain presence placeholder mismatch")
	})
}

func (s *JSONAsserterTestSuite) TestInvalidJSON() {
	ja := NewJSONAsserter(s.T())

	s.Run("invalid expected JSON", func() {
		actualJSON := `{"valid": "json"}`
		expectedJSON := `{"invalid": json}` // missing quotes

		diff := ja.diff(actualJSON, expectedJSON)
		s.Contains(diff, "invalid expected JSON", "Expected error about invalid expected JSON")
	})

	s.Run("invalid actual JSON", func() {
		actualJSON := `{"invalid": json}` // missing quotes
		expectedJSON := `{"valid": "json"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Contains(diff, "invalid actual JSON", "Expected error about invalid actual JSON")
	})
}

func (s *JSONAsserterTestSuite) TestCompareOnlyExpectedKeys() {
	s.Run("compares only expected keys when enabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
			WithIgnoreExtraKeys(false), // disable to ensure CompareOnlyExpectedKeys takes precedence
		)

		actualJSON := `{"id": "123", "name": "test", "extra": "value", "another_extra": 42}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with CompareOnlyExpectedKeys enabled")
	})

	s.Run("detects differences in expected keys when enabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
		)

		actualJSON := `{"id": "123", "name": "wrong", "extra": "value"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff for mismatched expected key values")
		s.Contains(diff, "name", "Expected diff to mention 'name' field")
	})

	s.Run("handles nested objects with CompareOnlyExpectedKeys", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
		)

		actualJSON := `{
			"device": {
				"id": "123",
				"name": "test",
				"extra_nested": "ignored"
			},
			"extra_top_level": "ignored"
		}`
		expectedJSON := `{
			"device": {
				"id": "123",
				"name": "test"
			}
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with nested CompareOnlyExpectedKeys")
	})

	s.Run("works with arrays and CompareOnlyExpectedKeys", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
		)

		actualJSON := `{
			"devices": [
				{"id": "1", "name": "device1", "extra": "ignored"},
				{"id": "2", "name": "device2", "extra": "ignored"}
			],
			"extra_field": "ignored"
		}`
		expectedJSON := `{
			"devices": [
				{"id": "1", "name": "device1"},
				{"id": "2", "name": "device2"}
			]
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with array CompareOnlyExpectedKeys")
	})

	s.Run("combines with other options", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithCompareOnlyExpectedKeys(true),
			WithAllowPresencePlaceholder(true),
			WithNilToEmptyArray(true),
		)

		actualJSON := `{
			"id": "123",
			"timestamp": 1758348286,
			"services": null,
			"extra_field": "ignored"
		}`
		expectedJSON := `{
			"id": "123",
			"timestamp": "<<PRESENCE>>",
			"services": []
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with combined options")
	})

	s.Run("disabled by default (standard behavior)", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreExtraKeys(false), // ensure extra keys cause failure
		)

		actualJSON := `{"id": "123", "name": "test", "extra": "value"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with CompareOnlyExpectedKeys disabled (default)")
	})
}

func (s *JSONAsserterTestSuite) TestNilToEmptyArrayBehavior() {
	s.Run("If the expected value is null, the actual value remains null, regardless of NilToEmptyArray", func() {
		ja := NewJSONAsserter(s.T()) // default options with NilToEmptyArray=true

		actualJSON := `{"null_value": null}`
		expectedJSON := `{"null_value": null}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "null should equal null")
	})

	s.Run("When NilToEmptyArray is enabled, a null actual value will be normalized to an empty array if the expected value is an empty array", func() {
		ja := NewJSONAsserter(s.T()) // default options with NilToEmptyArray=true

		actualJSON := `{"null_value": null}`
		expectedJSON := `{"null_value": []}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "null should be normalized to [] when NilToEmptyArray=true")
	})

	s.Run("When NilToEmptyArray is disabled, a null actual value should remain distinct from an empty array expected value", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithNilToEmptyArray(false),
		)

		actualJSON := `{"null_value": null}`
		expectedJSON := `{"null_value": []}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "null should NOT equal [] when NilToEmptyArray=false")
	})
}

func (s *JSONAsserterTestSuite) TestIgnoredFields() {
	s.Run("WithIgnoredFields sets fields correctly", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp", "debug_info"),
		)
		opts := ja.GetOptions()

		expectedFields := []string{"timestamp", "debug_info"}
		s.Equal(len(expectedFields), len(opts.IgnoredFields), "Expected correct number of ignored fields")
		for i, field := range expectedFields {
			s.Equal(field, opts.IgnoredFields[i], "Expected ignored field %s at index %d", field, i)
		}
	})

	s.Run("ignores specified fields at top level", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp", "debug_info"),
		)

		actualJSON := `{
			"id": "123",
			"name": "test",
			"timestamp": 1758348286,
			"debug_info": "some debug data"
		}`
		expectedJSON := `{
			"id": "123",
			"name": "test",
			"timestamp": 9999999999,
			"debug_info": "different debug data"
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with ignored fields")
	})

	s.Run("still detects differences in non-ignored fields", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp"),
		)

		actualJSON := `{
			"id": "123",
			"name": "wrong",
			"timestamp": 1758348286
		}`
		expectedJSON := `{
			"id": "123",
			"name": "test",
			"timestamp": 9999999999
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff for non-ignored field differences")
		s.Contains(diff, "name", "Expected diff to mention 'name' field")
	})

	s.Run("ignores fields in nested objects", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp", "debug_info"),
		)

		actualJSON := `{
			"device": {
				"id": "123",
				"name": "test",
				"timestamp": 1758348286,
				"debug_info": "nested debug"
			},
			"timestamp": 9876543210,
			"status": "active"
		}`
		expectedJSON := `{
			"device": {
				"id": "123",
				"name": "test",
				"timestamp": 1111111111,
				"debug_info": "different nested debug"
			},
			"timestamp": 5555555555,
			"status": "active"
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with ignored nested fields")
	})

	s.Run("ignores fields in arrays of objects", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp", "debug_info"),
		)

		actualJSON := `{
			"devices": [
				{
					"id": "1",
					"name": "device1",
					"timestamp": 1758348286,
					"debug_info": "debug1"
				},
				{
					"id": "2",
					"name": "device2",
					"timestamp": 1758348287,
					"debug_info": "debug2"
				}
			]
		}`
		expectedJSON := `{
			"devices": [
				{
					"id": "1",
					"name": "device1",
					"timestamp": 9999999999,
					"debug_info": "different debug1"
				},
				{
					"id": "2",
					"name": "device2",
					"timestamp": 8888888888,
					"debug_info": "different debug2"
				}
			]
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with ignored array fields")
	})

	s.Run("combines with other options", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("timestamp"),
			WithAllowPresencePlaceholder(true),
			WithNilToEmptyArray(true),
			WithIgnoreExtraKeys(true),
		)

		actualJSON := `{
			"id": "123",
			"name": "test",
			"timestamp": 1758348286,
			"services": null,
			"extra_field": "ignored",
			"other_timestamp": 1234567890
		}`
		expectedJSON := `{
			"id": "123",
			"name": "test",
			"timestamp": 9999999999,
			"services": [],
			"other_timestamp": "<<PRESENCE>>"
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with combined options and ignored fields")
	})

	s.Run("works when only some fields need to be ignored", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields("created_at"),
		)

		actualJSON := `{
			"id": "123",
			"name": "test",
			"created_at": "2023-01-01T00:00:00Z",
			"updated_at": "2023-01-02T00:00:00Z"
		}`
		expectedJSON := `{
			"id": "123",
			"name": "test",
			"created_at": "2024-01-01T00:00:00Z",
			"updated_at": "2023-01-02T00:00:00Z"
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with created_at ignored")
	})

	s.Run("empty ignored fields list works normally", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields(), // empty list
		)

		actualJSON := `{"id": "123", "name": "test"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with empty ignored fields")
	})

	s.Run("detects diff when ignored fields are empty and JSONs differ", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoredFields(), // empty list
		)

		actualJSON := `{"id": "123", "name": "wrong"}`
		expectedJSON := `{"id": "123", "name": "test"}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with empty ignored fields and different values")
	})
}

func (s *JSONAsserterTestSuite) TestIgnoreArrayOrder() {
	s.Run("arrays with same elements in different order match when enabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"items": [3, 1, 2]}`
		expectedJSON := `{"items": [1, 2, 3]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with IgnoreArrayOrder enabled")
	})

	s.Run("arrays with same elements in different order fail when disabled", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(false),
		)

		actualJSON := `{"items": [3, 1, 2]}`
		expectedJSON := `{"items": [1, 2, 3]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff with IgnoreArrayOrder disabled")
	})

	s.Run("arrays with different elements fail regardless of option", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"items": [1, 2, 3]}`
		expectedJSON := `{"items": [1, 2, 4]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.NotEmpty(diff, "Expected diff for different array elements even with IgnoreArrayOrder enabled")
	})

	s.Run("nested arrays are sorted correctly", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"data": [{"values": [3, 2, 1]}, {"values": [6, 5, 4]}]}`
		expectedJSON := `{"data": [{"values": [1, 2, 3]}, {"values": [4, 5, 6]}]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with nested arrays sorted")
	})

	s.Run("objects in arrays sorted by JSON representation", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"devices": [{"id": "2", "name": "b"}, {"id": "1", "name": "a"}]}`
		expectedJSON := `{"devices": [{"id": "1", "name": "a"}, {"id": "2", "name": "b"}]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with object arrays sorted")
	})

	s.Run("mixed nested structures with arrays and objects", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{
			"services": [
				{"id": "2", "chars": ["d", "c"]},
				{"id": "1", "chars": ["b", "a"]}
			]
		}`
		expectedJSON := `{
			"services": [
				{"id": "1", "chars": ["a", "b"]},
				{"id": "2", "chars": ["c", "d"]}
			]
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with mixed nested structures")
	})

	s.Run("empty arrays match regardless of order option", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"items": []}`
		expectedJSON := `{"items": []}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff for empty arrays")
	})

	s.Run("single element arrays match regardless of order", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		actualJSON := `{"items": [1]}`
		expectedJSON := `{"items": [1]}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff for single element arrays")
	})

	s.Run("combines with IgnoredFields option", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
			WithIgnoredFields("timestamp"),
		)

		actualJSON := `{
			"events": [
				{"id": "2", "timestamp": 2000},
				{"id": "1", "timestamp": 1000}
			]
		}`
		expectedJSON := `{
			"events": [
				{"id": "1", "timestamp": 9999},
				{"id": "2", "timestamp": 8888}
			]
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff with IgnoreArrayOrder + IgnoredFields")
	})

	s.Run("BLE notification use case - multiple characteristics", func() {
		ja := NewJSONAsserter(s.T()).WithOptions(
			WithIgnoreArrayOrder(true),
		)

		// Wrap arrays in an object since jsonassert doesn't support root-level arrays
		actualJSON := `{
			"array": [
				{"record": {"Values": {"6e400003b5a3f393e0a9e50e24dcca9e": "Hello"}}},
				{"record": {"Values": {"2a19": "*"}}},
				{"record": {"Values": {"6e400002b5a3f393e0a9e50e24dcca9e": "World"}}}
			]
		}`
		expectedJSON := `{
			"array": [
				{"record": {"Values": {"6e400003b5a3f393e0a9e50e24dcca9e": "Hello"}}},
				{"record": {"Values": {"6e400002b5a3f393e0a9e50e24dcca9e": "World"}}},
				{"record": {"Values": {"2a19": "*"}}}
			]
		}`

		diff := ja.diff(actualJSON, expectedJSON)
		s.Empty(diff, "Expected no diff for BLE notification case")
	})
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayComparison() {
	// GOAL: Verify JSONAsserter automatically wraps root-level arrays for comparison
	//
	// TEST SCENARIO: Compare two identical root-level arrays → no diff returned → assertion passes

	ja := NewJSONAsserter(s.T())

	actualJSON := `[{"id": "1", "name": "test"}, {"id": "2", "name": "test2"}]`
	expectedJSON := `[{"id": "1", "name": "test"}, {"id": "2", "name": "test2"}]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for identical root-level arrays")
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayDifference() {
	// GOAL: Verify JSONAsserter detects differences in root-level arrays
	//
	// TEST SCENARIO: Compare two different root-level arrays → diff returned → assertion fails

	ja := NewJSONAsserter(s.T())

	actualJSON := `[{"id": "1", "name": "actual"}, {"id": "2", "name": "test2"}]`
	expectedJSON := `[{"id": "1", "name": "expected"}, {"id": "2", "name": "test2"}]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.NotEmpty(diff, "Expected diff for different root-level arrays")
	s.Contains(diff, "name", "Expected diff to contain 'name' field difference")
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayWithIgnoreArrayOrder() {
	// GOAL: Verify IgnoreArrayOrder option works with root-level arrays
	//
	// TEST SCENARIO: Compare root-level arrays with same elements in different order with IgnoreArrayOrder=true → no diff → assertion passes

	ja := NewJSONAsserter(s.T()).WithOptions(
		WithIgnoreArrayOrder(true),
	)

	actualJSON := `[{"id": "2", "name": "b"}, {"id": "1", "name": "a"}]`
	expectedJSON := `[{"id": "1", "name": "a"}, {"id": "2", "name": "b"}]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for root-level arrays with IgnoreArrayOrder")
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayWithIgnoredFields() {
	// GOAL: Verify IgnoredFields option works with root-level arrays
	//
	// TEST SCENARIO: Compare root-level arrays with different ignored field values → no diff → assertion passes

	ja := NewJSONAsserter(s.T()).WithOptions(
		WithIgnoredFields("timestamp"),
	)

	actualJSON := `[{"id": "1", "timestamp": 1000}, {"id": "2", "timestamp": 2000}]`
	expectedJSON := `[{"id": "1", "timestamp": 9999}, {"id": "2", "timestamp": 8888}]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for root-level arrays with IgnoredFields")
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayVsObject() {
	// GOAL: Verify JSONAsserter correctly fails when comparing root-level array with root-level object
	//
	// TEST SCENARIO: Compare root-level array with root-level object → diff returned → assertion fails

	ja := NewJSONAsserter(s.T())

	actualJSON := `[{"id": "1"}]`
	expectedJSON := `{"id": "1"}`

	diff := ja.diff(actualJSON, expectedJSON)
	s.NotEmpty(diff, "Expected diff when comparing root-level array with object")
}

func (s *JSONAsserterTestSuite) TestEmptyRootLevelArrays() {
	// GOAL: Verify JSONAsserter handles empty root-level arrays correctly
	//
	// TEST SCENARIO: Compare two empty root-level arrays → no diff → assertion passes

	ja := NewJSONAsserter(s.T())

	actualJSON := `[]`
	expectedJSON := `[]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for empty root-level arrays")
}

func (s *JSONAsserterTestSuite) TestRootLevelArrayWithMixedOptions() {
	// GOAL: Verify multiple options work together with root-level arrays
	//
	// TEST SCENARIO: Compare root-level arrays with IgnoreArrayOrder + IgnoredFields → no diff → assertion passes

	ja := NewJSONAsserter(s.T()).WithOptions(
		WithIgnoreArrayOrder(true),
		WithIgnoredFields("timestamp", "debug"),
	)

	actualJSON := `[
		{"id": "2", "timestamp": 2000, "debug": "info2"},
		{"id": "1", "timestamp": 1000, "debug": "info1"}
	]`
	expectedJSON := `[
		{"id": "1", "timestamp": 9999, "debug": "different1"},
		{"id": "2", "timestamp": 8888, "debug": "different2"}
	]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for root-level arrays with mixed options")
}

func (s *JSONAsserterTestSuite) TestNestedArraysStillWork() {
	// GOAL: Verify nested arrays within objects continue to work correctly (regression test)
	//
	// TEST SCENARIO: Compare objects containing nested arrays → no diff → assertion passes

	ja := NewJSONAsserter(s.T())

	actualJSON := `{"items": [1, 2, 3], "data": {"values": [4, 5, 6]}}`
	expectedJSON := `{"items": [1, 2, 3], "data": {"values": [4, 5, 6]}}`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for nested arrays within objects")
}

func (s *JSONAsserterTestSuite) TestIgnoredFieldsRemovedBeforeSorting() {
	// GOAL: Verify ignored fields are removed BEFORE sorting arrays
	//
	// TEST SCENARIO: Compare arrays with identical data but different ignored field values → no diff → assertion passes
	//
	// This test ensures the critical ordering: removeIgnoredFields() BEFORE sortArrays().
	// If ignored fields are present during sorting, they affect the sort order even though
	// they're ignored during comparison, breaking IgnoreArrayOrder functionality.
	//
	// In the device_test: Two BLE notifications with same data but different call_counts would sort
	// in opposite order if call_count is included in the sort key.

	ja := NewJSONAsserter(s.T()).WithOptions(
		WithIgnoreArrayOrder(true),
		WithIgnoredFields("call_count"),
	)

	// Arrays with same data but different call_count values.
	// The call_count values would cause different sort orders if included in sorting:
	// - actual: {"call_count":1,"char":"B"} < {"call_count":2,"char":"A"} (sorts as B,A)
	// - expected: {"call_count":1,"char":"A"} < {"call_count":2,"char":"B"} (sorts as A,B)
	actualJSON := `{
		"notifications": [
			{"char": "B", "call_count": 1},
			{"char": "A", "call_count": 2}
		]
	}`
	expectedJSON := `{
		"notifications": [
			{"char": "A", "call_count": 1},
			{"char": "B", "call_count": 2}
		]
	}`

	// CORRECT behavior (remove ignored fields BEFORE sorting):
	// 1. Remove call_count: actual=[{"char":"B"},{"char":"A"}], expected=[{"char":"A"},{"char":"B"}]
	// 2. Sort both: actual=[{"char":"A"},{"char":"B"}], expected=[{"char":"A"},{"char":"B"}]
	// 3. Compare: MATCH ✅
	//
	// INCORRECT behavior (sort BEFORE removing ignored fields):
	// 1. Sort with call_count: actual=[{"char":"B","call_count":1},{"char":"A","call_count":2}]
	//                          expected=[{"char":"A","call_count":1},{"char":"B","call_count":2}]
	// 2. Remove call_count: actual=[{"char":"B"},{"char":"A"}], expected=[{"char":"A"},{"char":"B"}]
	// 3. Compare: FAIL ❌ - arrays in different orders!

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff when ignored fields removed before sorting.\n"+
		"This indicates ignored fields were NOT removed before sorting, breaking IgnoreArrayOrder")
}

func (s *JSONAsserterTestSuite) TestRootLevelPrimitiveArrays() {
	// GOAL: Verify root-level arrays of primitives are compared correctly
	//
	// TEST SCENARIO: Compare root-level arrays of primitive values → no diff → assertion passes

	ja := NewJSONAsserter(s.T())

	actualJSON := `[1, 2, 3, "test", true]`
	expectedJSON := `[1, 2, 3, "test", true]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.Empty(diff, "Expected no diff for root-level primitive arrays")
}

func (s *JSONAsserterTestSuite) TestRootLevelPrimitiveArrayDifference() {
	// GOAL: Verify differences in root-level primitive arrays are detected
	//
	// TEST SCENARIO: Compare root-level primitive arrays with different values → diff returned → assertion fails

	ja := NewJSONAsserter(s.T())

	actualJSON := `[1, 2, 3]`
	expectedJSON := `[1, 2, 4]`

	diff := ja.diff(actualJSON, expectedJSON)
	s.NotEmpty(diff, "Expected diff for different root-level primitive arrays")
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					indexOfString(s, substr) >= 0)))
}

// Simple string search function
func indexOfString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestJSONAsserterSuite(t *testing.T) {
	suite.Run(t, new(JSONAsserterTestSuite))
}
