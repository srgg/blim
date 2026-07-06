//go:build test

package testutils

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/mcuadros/go-defaults"
	"github.com/srgg/blim/internal/device"
	"github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"
)

func MustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type JSONAssertOptions struct {
	IgnoreExtraKeys          bool     `default:"true"`
	NilToEmptyArray          bool     `default:"true"`
	AllowPresencePlaceholder bool     `default:"true"`
	CompareOnlyExpectedKeys  bool     `default:"false"`
	IgnoredFields            []string `default:""`
	IgnoreArrayOrder         bool     `default:"false"`
}

// Option is a functional option for configuring JSONAsserter
type Option func(*JSONAssertOptions)

type JSONAsserter struct {
	t       *testing.T
	options JSONAssertOptions
}

// NewJSONAsserter creates a new JSONAsserter with default options
func NewJSONAsserter(t *testing.T) *JSONAsserter {
	opts := JSONAssertOptions{}
	defaults.SetDefaults(&opts)
	return &JSONAsserter{
		t:       t,
		options: opts,
	}
}

// WithOptions applies functional options to the JSONAsserter
func (ja *JSONAsserter) WithOptions(opts ...Option) *JSONAsserter {
	for _, opt := range opts {
		opt(&ja.options)
	}
	return ja
}

// WithOptionsStruct method for backward compatibility
func (ja *JSONAsserter) WithOptionsStruct(opts JSONAssertOptions) *JSONAsserter {
	ja.options.IgnoreExtraKeys = opts.IgnoreExtraKeys
	ja.options.NilToEmptyArray = opts.NilToEmptyArray
	ja.options.AllowPresencePlaceholder = opts.AllowPresencePlaceholder
	ja.options.CompareOnlyExpectedKeys = opts.CompareOnlyExpectedKeys
	ja.options.IgnoredFields = opts.IgnoredFields
	ja.options.IgnoreArrayOrder = opts.IgnoreArrayOrder
	return ja
}

// GetOptions returns a copy of the current options (for testing)
func (ja *JSONAsserter) GetOptions() JSONAssertOptions {
	return ja.options
}

// Assert compares actualJSON against expectedJSON
func (ja *JSONAsserter) Assert(actualJSON, expectedJSON string) {
	diff := ja.diff(actualJSON, expectedJSON)
	if diff != "" {
		ja.t.Errorf("JSON assertion failed:\n%s", diff)
	}
}

func (ja *JSONAsserter) AssertErr(actualJSON, expectedJSON string) error {
	diff := ja.diff(actualJSON, expectedJSON)
	if diff != "" {
		return fmt.Errorf("JSON assertion failed:\n%s", diff)
	}
	return nil
}

// AssertDevice compares actual Device against expectedJSON
func (ja *JSONAsserter) AssertDevice(dev device.Device, expectedJSON string) {
	actualJSON := DeviceToJSON(dev)
	ja.Assert(actualJSON, expectedJSON)
}

func (ja *JSONAsserter) diff(actualJSON, expectedJSON string) string {
	// Always unmarshal into fresh copies
	var expected, actual interface{}
	if err := json.Unmarshal([]byte(expectedJSON), &expected); err != nil {
		return fmt.Sprintf("invalid expected JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(actualJSON), &actual); err != nil {
		return fmt.Sprintf("invalid actual JSON: %v", err)
	}

	// Wrap root-level arrays in objects (gojsondiff limitation workaround)
	// This makes array comparison transparent to callers
	if isArray(expected) && isArray(actual) {
		expected = map[string]interface{}{"array": expected}
		actual = map[string]interface{}{"array": actual}
	}

	// Apply placeholder logic only if allowed
	if ja.options.AllowPresencePlaceholder {
		replacePresenceWithActual(expected, actual)
	}

	// Apply other normalization options
	if ja.options.NilToEmptyArray {
		normalizeNilArrays(expected, actual)
	}

	// CRITICAL: Remove ignored fields BEFORE sorting arrays.
	//
	// sortArrays() sorts by JSON representation of elements. If ignored fields
	// (e.g., call_count, timestamp) are present during sorting, they will affect
	// the sort order even though they're ignored during comparison. This causes
	// elements with identical content but different ignored field values to sort
	// differently, breaking the IgnoreArrayOrder functionality.
	//
	// Example: Two BLE notifications with the same data but different call_counts
	// would sort in opposite order if call_count is included in the sort key.
	if len(ja.options.IgnoredFields) > 0 {
		removeIgnoredFields(expected, actual, ja.options.IgnoredFields)
	}
	// Sort arrays BEFORE pruning extra keys so elements align correctly
	if ja.options.IgnoreArrayOrder {
		sortArrays(expected)
		sortArrays(actual)
	}
	if ja.options.IgnoreExtraKeys {
		pruneExtraKeys(actual, expected)
	}
	if ja.options.CompareOnlyExpectedKeys {
		extractOnlyExpectedKeys(actual, expected)
	}

	//// Marshal back to bytes for diff
	expectedBytes, _ := json.Marshal(expected)
	actualBytes, _ := json.Marshal(actual)

	// Code uses github.com/yudai/gojsondiff
	differ := gojsondiff.New()
	diff, err := differ.Compare(expectedBytes, actualBytes)
	if err != nil {
		return fmt.Sprintf("JSON comparison failed: %v", err)
	}

	if !diff.Modified() {
		return ""
	}

	config := formatter.AsciiFormatterConfig{
		ShowArrayIndex: true,
		Coloring:       false,
	}
	f := formatter.NewAsciiFormatter(expected, config)
	diffString, _ := f.Format(diff)
	return diffString
}

// replacePresenceWithActual copies actual values for "<<PRESENCE>>" placeholders
func replacePresenceWithActual(expected, actual interface{}) {
	switch exp := expected.(type) {
	case map[string]interface{}:
		act, ok := actual.(map[string]interface{})
		if !ok {
			return
		}
		for k := range exp {
			if s, ok := exp[k].(string); ok && s == "<<PRESENCE>>" {
				exp[k] = act[k] // copy actual value for comparison
			} else {
				replacePresenceWithActual(exp[k], act[k])
			}
		}
	case []interface{}:
		act, ok := actual.([]interface{})
		if !ok {
			return
		}
		for i := range exp {
			if i < len(act) {
				replacePresenceWithActual(exp[i], act[i])
			}
		}
	}
}

// Normalize nil slices to empty slices, but only when both sides can be normalized
func normalizeNilArrays(expected, actual interface{}) {
	switch exp := expected.(type) {
	case map[string]interface{}:
		act, ok := actual.(map[string]interface{})
		if !ok {
			return
		}
		for k := range exp {
			expVal := exp[k]
			actVal := act[k]

			// Only normalize if both values are nil, or one is nil and the other is empty array
			if shouldNormalize(expVal, actVal) {
				if expVal == nil {
					exp[k] = []interface{}{}
				}
				if actVal == nil {
					act[k] = []interface{}{}
				}
			} else if expVal != nil && actVal != nil {
				// Recursively normalize nested objects
				if s, ok := expVal.(string); !ok || s != "<<PRESENCE>>" {
					normalizeNilArrays(expVal, actVal)
				}
			}
		}
	case []interface{}:
		act, ok := actual.([]interface{})
		if !ok {
			return
		}
		for i := range exp {
			if i < len(act) {
				if shouldNormalize(exp[i], act[i]) {
					if exp[i] == nil {
						exp[i] = []interface{}{}
					}
					if act[i] == nil {
						act[i] = []interface{}{}
					}
				} else if exp[i] != nil && act[i] != nil {
					normalizeNilArrays(exp[i], act[i])
				}
			}
		}
	}
}

// shouldNormalize determines if null values should be converted to empty arrays
// Only normalize when it makes semantic sense (both are nil/empty, or one is nil and other is empty array)
func shouldNormalize(expectedVal, actualVal interface{}) bool {
	// Both are nil - normalize both to []
	if expectedVal == nil && actualVal == nil {
		return true
	}

	// One is nil, other is empty array - normalize the nil one
	if expectedVal == nil {
		if arr, ok := actualVal.([]interface{}); ok && len(arr) == 0 {
			return true
		}
	}
	if actualVal == nil {
		if arr, ok := expectedVal.([]interface{}); ok && len(arr) == 0 {
			return true
		}
	}

	// Don't normalize in other cases (e.g., one is nil, other is non-empty array or different type)
	return false
}

// Remove keys in actual that don’t exist in expected
func pruneExtraKeys(actual, expected interface{}) {
	switch exp := expected.(type) {
	case map[string]interface{}:
		act, ok := actual.(map[string]interface{})
		if !ok {
			return
		}
		for k := range act {
			if _, exists := exp[k]; !exists {
				delete(act, k)
			}
		}
		for k := range exp {
			pruneExtraKeys(act[k], exp[k])
		}
	case []interface{}:
		act, ok := actual.([]interface{})
		if !ok {
			return
		}
		for i := range exp {
			if i < len(act) {
				pruneExtraKeys(act[i], exp[i])
			}
		}
	}
}

// Extract only keys from actual that exist in expected, creating a new structure
func extractOnlyExpectedKeys(actual, expected interface{}) interface{} {
	switch exp := expected.(type) {
	case map[string]interface{}:
		act, ok := actual.(map[string]interface{})
		if !ok {
			return actual
		}
		extracted := make(map[string]interface{})
		for k := range exp {
			if v, exists := act[k]; exists {
				extracted[k] = extractOnlyExpectedKeys(v, exp[k])
			}
		}
		// Replace actual with extracted data
		for k := range act {
			delete(act, k)
		}
		for k, v := range extracted {
			act[k] = v
		}
		return act
	case []interface{}:
		act, ok := actual.([]interface{})
		if !ok {
			return actual
		}
		for i := range exp {
			if i < len(act) {
				act[i] = extractOnlyExpectedKeys(act[i], exp[i])
			}
		}
		return act
	}
	return actual
}

// Remove fields from both expected and actual that are in the ignored fields list
func removeIgnoredFields(expected, actual interface{}, ignoredFields []string) {
	if len(ignoredFields) == 0 {
		return
	}

	switch exp := expected.(type) {
	case map[string]interface{}:
		act, ok := actual.(map[string]interface{})
		if !ok {
			return
		}

		// Remove ignored fields from both expected and actual
		for _, field := range ignoredFields {
			delete(exp, field)
			delete(act, field)
		}

		// Recursively process nested objects
		for k := range exp {
			if actVal, exists := act[k]; exists {
				removeIgnoredFields(exp[k], actVal, ignoredFields)
			}
		}
	case []interface{}:
		act, ok := actual.([]interface{})
		if !ok {
			return
		}

		// Recursively process array elements
		for i := range exp {
			if i < len(act) {
				removeIgnoredFields(exp[i], act[i], ignoredFields)
			}
		}
	}
}

// Functional option constructors

// WithIgnoreExtraKeys sets whether to ignore extra keys in actual JSON
func WithIgnoreExtraKeys(ignore bool) Option {
	return func(opts *JSONAssertOptions) {
		opts.IgnoreExtraKeys = ignore
	}
}

// WithNilToEmptyArray sets whether to normalize nil arrays to empty arrays
func WithNilToEmptyArray(normalize bool) Option {
	return func(opts *JSONAssertOptions) {
		opts.NilToEmptyArray = normalize
	}
}

// WithAllowPresencePlaceholder sets whether to allow "<<PRESENCE>>" placeholders
func WithAllowPresencePlaceholder(allow bool) Option {
	return func(opts *JSONAssertOptions) {
		opts.AllowPresencePlaceholder = allow
	}
}

// WithCompareOnlyExpectedKeys sets whether to allow "<<PRESENCE>>" placeholders
func WithCompareOnlyExpectedKeys(allow bool) Option {
	return func(opts *JSONAssertOptions) {
		opts.CompareOnlyExpectedKeys = allow
	}
}

// WithIgnoredFields sets a list of field names to ignore during comparison
func WithIgnoredFields(fields ...string) Option {
	return func(opts *JSONAssertOptions) {
		opts.IgnoredFields = fields
	}
}

// WithIgnoreArrayOrder sets whether to ignore array element order during comparison
func WithIgnoreArrayOrder(ignore bool) Option {
	return func(opts *JSONAssertOptions) {
		opts.IgnoreArrayOrder = ignore
	}
}

// isArray checks if the given interface is a JSON array ([]interface{})
func isArray(v interface{}) bool {
	_, ok := v.([]interface{})
	return ok
}

// sortArrays recursively sorts arrays in JSON structures for order-independent comparison.
// Arrays are sorted by the JSON representation of their elements to ensure consistent ordering.
func sortArrays(data interface{}) {
	switch v := data.(type) {
	case map[string]interface{}:
		// Recursively sort arrays in nested objects
		for key := range v {
			sortArrays(v[key])
		}
	case []interface{}:
		// Sort array by JSON representation of elements
		sort.Slice(v, func(i, j int) bool {
			iJSON, _ := json.Marshal(v[i])
			jJSON, _ := json.Marshal(v[j])
			return string(iJSON) < string(jJSON)
		})
		// Recursively sort nested structures within array elements
		for _, elem := range v {
			sortArrays(elem)
		}
	}
}
