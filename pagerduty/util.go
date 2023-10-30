package pagerduty

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func timeToUTC(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}

	return t.UTC(), nil
}

// validateRFC3339 validates that a date string has the correct RFC3339 layout
func validateRFC3339(v interface{}, k string) (we []string, errors []error) {
	value := v.(string)
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		errors = append(errors, genErrorTimeFormatRFC339(value, k))
	}
	if t.Second() > 0 {
		errors = append(errors, fmt.Errorf("please set the time %s to a full minute, e.g. 11:23:00, not 11:23:05", value))
	}

	return
}

func genErrorTimeFormatRFC339(value, k string) error {
	return fmt.Errorf("%s is not a valid format for argument: %s. Expected format: %s (RFC3339)", value, k, time.RFC3339)
}

func suppressRFC3339Diff(k, oldTime, newTime string, d *schema.ResourceData) bool {
	oldT, newT, err := parseRFC3339Time(k, oldTime, newTime)
	if err != nil {
		log.Printf(err.Error())
		return false
	}

	return oldT.Equal(newT)
}

// issue: https://github.com/PagerDuty/terraform-provider-pagerduty/issues/200
// The start value of schedule layer can't be set to a time in the past. So if the value passed in is before the current time then PagerDuty
// will set the start to the current time. Thus, we do not need to show diff if both newT and oldT is in the past, as it will not bring
// any real changes to the schedule layer.
func suppressScheduleLayerStartDiff(k, oldTime, newTime string, d *schema.ResourceData) bool {
	oldT, newT, err := parseRFC3339Time(k, oldTime, newTime)
	if err != nil {
		log.Printf(err.Error())
		return false
	}

	return oldT.Equal(newT) || (newT.Before(time.Now()) && oldT.Before(time.Now()))
}

func parseRFC3339Time(k, oldTime, newTime string) (time.Time, time.Time, error) {
	var t time.Time
	oldT, err := time.Parse(time.RFC3339, oldTime)
	if err != nil {
		return t, t, fmt.Errorf("[ERROR] Failed to parse %q (old %q). Expected format: %s (RFC3339)", oldTime, k, time.RFC3339)
	}

	newT, err := time.Parse(time.RFC3339, newTime)
	if err != nil {
		return t, t, fmt.Errorf("[ERROR] Failed to parse %q (new %q). Expected format: %s (RFC3339)", oldTime, k, time.RFC3339)
	}

	return oldT, newT, nil
}

func suppressLeadTrailSpaceDiff(k, old, new string, d *schema.ResourceData) bool {
	return old == strings.TrimSpace(new)
}

func suppressCaseDiff(k, old, new string, d *schema.ResourceData) bool {
	return old == strings.ToLower(new)
}

// Validate a value against a set of possible values
func validateValueDiagFunc(values []string) schema.SchemaValidateDiagFunc {
	return func(v interface{}, p cty.Path) diag.Diagnostics {
		var diags diag.Diagnostics

		value := v.(string)
		valid := false
		for _, val := range values {
			if value == val {
				valid = true
				break
			}
		}

		if !valid {
			diags = append(diags, diag.Diagnostic{
				Severity:      diag.Error,
				Summary:       fmt.Sprintf("%#v is an invalid value. Must be one of %#v", value, values),
				AttributePath: p,
			})
		}
		return diags
	}
}

type StringContentValidationMode int64

const (
	NoContentValidation StringContentValidationMode = iota
	NoNonPrintableChars
	NoNonPrintableCharsOrSpecialChars
)

// validateIsAllowedString will always validate if string provided is not empty,
// neither has trailing white spaces. Additionally the string content validation
// will be done based on the `mode` set.
//
//	mode: NoContentValidation | NoNonPrintableChars | NoNonPrintableCharsOrSpecialChars
func validateIsAllowedString(mode StringContentValidationMode) schema.SchemaValidateDiagFunc {
	return func(v interface{}, p cty.Path) diag.Diagnostics {
		var diags diag.Diagnostics

		fillDiags := func() {
			summary := "Name can not be blank. Trailing white spaces are not allowed either."
			switch mode {
			case NoNonPrintableChars:
				summary = "Name can not be blank, nor contain non-printable characters. Trailing white spaces are not allowed either."
			case NoNonPrintableCharsOrSpecialChars:
				summary = "Name can not be blank, nor contain the characters '\\', '/', '&', '<', '>', or any non-printable characters. Trailing white spaces are not allowed either."
			}
			diags = append(diags, diag.Diagnostic{
				Severity:      diag.Error,
				Summary:       summary,
				AttributePath: p,
			})
		}

		value := v.(string)
		if value == "" {
			fillDiags()
			return diags
		}

		for _, char := range value {
			if (mode == NoNonPrintableChars || mode == NoNonPrintableCharsOrSpecialChars) && !unicode.IsPrint(char) {
				fillDiags()
				return diags
			}
			if mode == NoNonPrintableCharsOrSpecialChars {
				switch char {
				case '\\', '/', '&', '<', '>':
					fillDiags()
					return diags
				}
			}
		}

		if strings.HasSuffix(value, " ") {
			fillDiags()
			return diags
		}

		return diags
	}
}

// Takes the result of flatmap.Expand for an array of strings
// and returns a []string
func expandStringList(configured []interface{}) []string {
	vs := make([]string, 0, len(configured))
	for _, v := range configured {
		vs = append(vs, string(v.(string)))
	}
	return vs
}

func expandString(v string) []interface{} {
	var obj []interface{}
	if err := json.Unmarshal([]byte(v), &obj); err != nil {
		log.Printf("[ERROR] Could not unmarshal field %s: %v", v, err)
		return nil
	}

	return obj
}

func flattenSlice(v []interface{}) interface{} {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[ERROR] Could not marshal field %s: %v", v, err)
		return nil
	}
	return string(b)
}

// stringTypeToStringPtr is a helper that returns a pointer to
// the string value passed in or nil if the string is empty.
func stringTypeToStringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// stringPtrToStringType is a helper that returns the string value passed in
// or an empty string if the given pointer is nil.
func stringPtrToStringType(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func intTypeToIntPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// renderRoundedPercentage is a helper function to render percentanges
// represented as float64 numbers, by its round with two decimals string
// representation.
func renderRoundedPercentage(p float64) string {
	return fmt.Sprintf("%.2f", math.Round(p*100))
}

// isNilFunc is a helper which verifies if an empty interface expecting a
// nullable value indeed has a `nil` type assigned or it's just empty.
func isNilFunc(i interface{}) bool {
	if i == nil {
		return true
	}
	switch reflect.TypeOf(i).Kind() {
	case reflect.Ptr, reflect.Map, reflect.Array, reflect.Chan, reflect.Slice:
		return reflect.ValueOf(i).IsNil()
	}
	return false
}

// unique will remove duplicates from a strings slice.
func unique(s []string) []string {
	result := []string{}
	uniqueVals := make(map[string]bool)
	for _, v := range s {
		if _, ok := uniqueVals[v]; !ok {
			uniqueVals[v] = true
			result = append(result, v)
		}
	}
	return result
}

func resourcePagerDutyParseColonCompoundID(id string) (string, string, error) {
	parts := strings.Split(id, ":")

	if len(parts) < 2 {
		return "", "", fmt.Errorf("%s: expected colon compound ID to have at least two components", id)
	}

	return parts[0], parts[1], nil
}
