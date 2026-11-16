package needle

import (
	"reflect"
	"strings"
	"testing"
)

// The expectations below were generated from the reference
// temporal-grounding implementation, so these tests are pinned to the
// exact expected semantics.

func TestSourceYears(t *testing.T) {
	cases := []struct {
		text  string
		years []int
	}{
		{"what's the weather in Paris", nil},
		{"due 5 may 2026", []int{2026}},
		{"due May 2026", []int{2026}},
		{"due 5th May 2026", []int{2026}},
		{"on 2026-07-21", []int{2026}},
		{"year 1999 was great", []int{1999}},
		{"12/05/2026", nil},
		{"12026/07/01", nil},
		{"may 20261", nil},
		{"invoice from 2026", nil},
		{"in the year 2026", []int{2026}},
		{"May, 2026", []int{2026}},
		{"sep 3, 2025 and october 2024", []int{2024, 2025}},
		{"21st August, 2024", []int{2024}},
		{"3 September 2025", []int{2025}},
		{"Dec 2024", []int{2024}},
		{"date: 2026-07-21 Tue 14:30", []int{2026}},
		{"from 07/21/2026 until 08/01/2026", nil},
		{"nothing here at all", nil},
		{"call me on 555-2026", nil},
		{"the 2026 bug", nil},
		{"march 3rd 2027", []int{2027}},
		{"January 1, 2000 through December 31, 2099", []int{2000, 2099}},
		{"42", nil},
		{"v2.0.4 released 2026-05-01", []int{2026}},
		{"25 Dec 2025", []int{2025}},
		{"25 December 2025", []int{2025}},
		{"Sept 2025", []int{2025}},
		{"sun 21 sep 2026", []int{2026}},
		{"20 26 split", nil},
	}
	for _, tc := range cases {
		got := sortedKeys(sourceYears(tc.text))
		if !reflect.DeepEqual(got, tc.years) {
			t.Errorf("sourceYears(%q) = %v, want %v", tc.text, got, tc.years)
		}
	}
}

func sortedKeys(m map[int]bool) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func TestWalkGrounding(t *testing.T) {
	// Ground truth for text "due 5 may 2026"
	// (licensed year: 2026) with 1999-valued dates.
	years := map[int]bool{2026: true}
	cases := []struct {
		name     string
		schema   map[string]any
		args     map[string]any
		checked  []string
		failures []string
	}{
		{
			name: "simple",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"due_date": map[string]any{"type": "string", "format": "date"}}},
			args:     map[string]any{"due_date": "1999-01-01"},
			checked:  []string{"due_date"},
			failures: []string{"due_date"},
		},
		{
			name: "nested",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"meta": map[string]any{"type": "object", "properties": map[string]any{
					"when": map[string]any{"type": "string", "format": "date-time"}}},
				"events": map[string]any{"type": "array", "items": map[string]any{
					"type": "object", "properties": map[string]any{
						"date": map[string]any{"type": "string", "format": "date"}}}}}},
			args: map[string]any{
				"meta":   map[string]any{"when": "1999-01-01T10:00:00Z"},
				"events": []any{map[string]any{"date": "2026-02-02"}, map[string]any{"date": "1999-03-03"}}},
			checked:  []string{"events[0].date", "events[1].date", "meta.when"},
			failures: []string{"events[1].date", "meta.when"},
		},
		{
			name: "anyof",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"when": map[string]any{"anyOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"type": "string", "format": "date"}}}}},
			args:     map[string]any{"when": "1999-01-01"},
			checked:  []string{"when"},
			failures: []string{"when"},
		},
		{
			name: "ref",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"when": map[string]any{"$ref": "#/$defs/date"}},
				"$defs": map[string]any{"date": map[string]any{"type": "string", "format": "date"}}},
			args:     map[string]any{"when": "1999-01-01"},
			checked:  []string{"when"},
			failures: []string{"when"},
		},
	}
	for _, tc := range cases {
		checked, failures := walkGrounding(tc.schema, tc.args, years)
		if got := sortedSet(checked); !reflect.DeepEqual(got, tc.checked) {
			t.Errorf("%s: checked = %v, want %v", tc.name, got, tc.checked)
		}
		if got := sortedSet(failures); !reflect.DeepEqual(got, tc.failures) {
			t.Errorf("%s: failures = %v, want %v", tc.name, got, tc.failures)
		}
	}
}

func TestValidateExtraction(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"due_date": map[string]any{"type": "string", "format": "date"}}}
	// Temporal contradiction: 1999 not licensed by "due 5 may 2026".
	err := validateExtraction("due 5 may 2026", schema,
		map[string]any{"due_date": "1999-01-01"}, &Response{}, "")
	if err == nil {
		t.Fatal("expected ExtractionValidationError for unlicensed year")
	}
	if !isExtractionValidationError(err) {
		t.Fatalf("error should wrap ExtractionValidationError, got %v", err)
	}
	// Grounded value passes.
	if err := validateExtraction("due 5 may 2026", schema,
		map[string]any{"due_date": "2026-05-05"}, &Response{}, ""); err != nil {
		t.Fatalf("grounded value should pass, got %v", err)
	}
	// No years mentioned: nothing is checked.
	if err := validateExtraction("no dates here", schema,
		map[string]any{"due_date": "1999-01-01"}, &Response{}, ""); err != nil {
		t.Fatalf("no licensed years means no temporal check, got %v", err)
	}
	// Engine-reported ungrounded and negation surface in strict mode.
	resp := &Response{Validation: &Validation{Ungrounded: []string{"extract.due_date"}}}
	err = validateExtraction("no dates here", schema,
		map[string]any{"due_date": "2026-01-01"}, resp, "")
	if !isExtractionValidationError(err) {
		t.Fatalf("engine ungrounded should fail strict validation, got %v", err)
	}
	resp = &Response{Validation: &Validation{Negation: true}}
	err = validateExtraction("anything", schema, map[string]any{}, resp, "")
	if !isExtractionValidationError(err) {
		t.Fatalf("negation should fail strict validation, got %v", err)
	}
}

func isExtractionValidationError(err error) bool {
	for err != nil {
		if err == ExtractionValidationError {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func TestAnnotateUngrounded(t *testing.T) {
	toolSchemas := []map[string]any{{
		"name": "pay_invoice",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"due_date": map[string]any{"type": "string", "format": "date"}}}}}
	r := &Response{
		Type: "call",
		FunctionCalls: []FunctionCall{{Name: "pay_invoice",
			Arguments: map[string]any{"due_date": "1999-01-01"}}},
	}
	annotateUngrounded(r, toolSchemas, sourceYears("due 5 may 2026"), "", "due 5 may 2026")
	if r.Validation == nil || len(r.Validation.Ungrounded) != 1 ||
		r.Validation.Ungrounded[0] != "pay_invoice.due_date" {
		t.Fatalf("validation = %+v", r.Validation)
	}
	// No licensed years: no annotation.
	r2 := &Response{
		Type: "call",
		FunctionCalls: []FunctionCall{{Name: "pay_invoice",
			Arguments: map[string]any{"due_date": "1999-01-01"}}},
	}
	annotateUngrounded(r2, toolSchemas, map[int]bool{}, "", "due 5 may 2026")
	if r2.Validation != nil {
		t.Fatalf("validation should stay nil, got %+v", r2.Validation)
	}
}

func TestUngroundedPaths(t *testing.T) {
	r := &Response{Validation: &Validation{Ungrounded: []string{
		"set_lights.brightness", "no_path", "set_lights.color"}}}
	grouped := ungroundedPaths(r)
	if len(grouped["set_lights"]) != 2 || !grouped["set_lights"]["brightness"] || !grouped["set_lights"]["color"] {
		t.Errorf("set_lights paths = %v", grouped["set_lights"])
	}
	if !grouped["no_path"]["no_path"] {
		t.Errorf("pathless entry should fall back to the tool name: %v", grouped["no_path"])
	}
}

// The matrix below mirrors the extraction-number cases of the Python
// reference suite: an engine-flagged numeric argument is cleared when its
// value literally occurs in the source (separators normalized), and
// rejected otherwise; string flags are never cleared by a number source.
func TestExtractNumberGrounding(t *testing.T) {
	invoiceSchema := map[string]any{"type": "object", "properties": map[string]any{
		"vendor":   map[string]any{"type": "string"},
		"total":    map[string]any{"type": "number"},
		"due_date": map[string]any{"type": "string", "format": "date"}}}
	invoiceArgs := func(total float64) map[string]any {
		return map[string]any{"vendor": "Acme Corp", "total": total, "due_date": "2026-09-01"}
	}
	dated := "Invoice from Acme Corp, $1,200.00, due 2026-09-01"
	cases := []struct {
		name    string
		text    string
		args    map[string]any
		flagged []string
		wantErr string // "" expects success
	}{
		{
			name:    "thousands separated number is cleared",
			text:    dated,
			args:    invoiceArgs(1200),
			flagged: []string{"extract.total"},
		},
		{
			name:    "fabricated number is rejected",
			text:    dated,
			args:    invoiceArgs(9999),
			flagged: []string{"extract.total"},
			wantErr: "total",
		},
		{
			name:    "non numeric flag is not cleared by a number source",
			text:    dated,
			args:    invoiceArgs(1200),
			flagged: []string{"extract.vendor"},
			wantErr: "vendor",
		},
		{
			name:    "number absent from the source is rejected",
			text:    "Invoice from Acme Corp, due 2026-09-01",
			args:    invoiceArgs(1200),
			flagged: []string{"extract.total"},
			wantErr: "total",
		},
		{
			name:    "a date hyphen is not a minus sign",
			text:    dated,
			args:    invoiceArgs(-9),
			flagged: []string{"extract.total"},
			wantErr: "total",
		},
		{
			name:    "a range hyphen is not a minus sign",
			text:    "Invoice from Acme Corp, $1,200.00-1,400.00, due 2026-09-01",
			args:    invoiceArgs(-1400),
			flagged: []string{"extract.total"},
			wantErr: "total",
		},
		{
			name:    "a written negative is cleared",
			text:    "Invoice from Acme Corp, -1,200.00, due 2026-09-01",
			args:    invoiceArgs(-1200),
			flagged: []string{"extract.total"},
		},
		{
			name:    "an unseparated number is cleared",
			text:    "Invoice from Acme Corp, $1200.00, due 2026-09-01",
			args:    invoiceArgs(1200),
			flagged: []string{"extract.total"},
		},
		{
			name:    "a digit run inside a longer number is rejected",
			text:    "Invoice from Acme Corp, $12,000.00, due 2026-09-01",
			args:    invoiceArgs(1200),
			flagged: []string{"extract.total"},
			wantErr: "total",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &Response{Validation: &Validation{Ungrounded: tc.flagged}}
			err := validateExtraction(tc.text, invoiceSchema, tc.args, resp, "")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the flagged value to be cleared, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected ExtractionValidationError mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSourceNumbers(t *testing.T) {
	// Date facts are stripped so their pieces never ground an argument.
	numbers := sourceNumbers(
		"date: 2026-07-21 Tue 14:30; total 1,200.00 and 7",
		"$1200.00-1,400.00 due 2026-09-01")
	want := map[string]bool{
		"1200": true, // 1,200.00 and $1200.00 normalize to the same value
		"7":    true,
		"1400": true, // the range hyphen does not become a minus
		"2026": true, // the bare due date's year
		"9":    true, // 09 of the bare due date
		"1":    true, // 01 of the bare due date
	}
	if len(numbers) != len(want) {
		t.Fatalf("numbers = %v, want %v", numbers, want)
	}
	for key := range want {
		if !numbers[key] {
			t.Errorf("numbers is missing %q: %v", key, numbers)
		}
	}
}
