package needle

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file implements the grounding checks: values the model emits for
// date arguments are only licensed by years that literally appear in the
// conversation or (when the input reasons relatively) the system facts,
// numbers the engine flags are exempt when their value occurs in the
// source text, and the engine's own ungrounded/negation signals are
// surfaced on the response.

var monthAlternation = `(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?`

// yearPatterns extract candidate year tokens. Some of them would need
// look-ahead/look-behind assertions in a general regex engine; Go's RE2
// does not support look-around, so the equivalent semantics are reproduced
// with post-match boundary checks in sourceYears (the assertions only test
// whether the characters around a match are digits or alphanumerics, and a
// shorter backtracked year is always followed by a digit of the longer
// run).
var yearPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{1,2}(?:st|nd|rd|th)?\s+` + monthAlternation + `[\s,]+(\d{1,4})`),
	regexp.MustCompile(`\b` + monthAlternation + `\s+\d{1,2}(?:st|nd|rd|th)?\s*,?\s+(\d{1,4})`),
	regexp.MustCompile(`\b` + monthAlternation + `[\s,]+(\d{3,4})`),
	regexp.MustCompile(`\byear\s+(\d{1,4})`),
	// (\d{4})[-/]mm[-/]dd with digit-boundary checks on both sides; the
	// year is exactly four digits.
	regexp.MustCompile(`(\d{4})[-/]\d{1,2}[-/]\d{1,2}`),
}

var yearSuffix = regexp.MustCompile(`^(\d{4})-`)

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// sourceYears extracts the literal years mentioned in a text: dates like
// "5 May 2026", "May 2026", "2026-07-21", explicit "year 1999" phrases,
// and the year part of date-shaped numbers.
func sourceYears(text string) map[int]bool {
	years := map[int]bool{}
	lowered := strings.ToLower(text)
	for i, re := range yearPatterns {
		for _, m := range re.FindAllStringSubmatchIndex(lowered, -1) {
			if len(m) < 4 {
				continue
			}
			start, end := m[0], m[1]
			switch i {
			case 0, 1:
				// Reproduce "(?![0-9A-Za-z]|:\d|\s*[ap]\.?m\b)" after
				// the year: an alphanumeric, a time suffix ("May 4 5:30")
				// or am/pm ("may 4, 5pm") means the group is not a year.
				if end < len(lowered) {
					c := lowered[end]
					if isASCIIAlnum(c) || (c == ':' && end+1 < len(lowered) && isDigit(lowered[end+1])) {
						continue
					}
					rest := strings.TrimLeft(lowered[end:], " ")
					if len(rest) > 0 && (rest[0] == 'a' || rest[0] == 'p') {
						if suffix := strings.TrimPrefix(rest[1:], "."); strings.HasPrefix(suffix, "m") {
							continue
						}
					}
				}
			case 2, 3:
				// Reproduce "(?![0-9A-Za-z])" after the year.
				if end < len(lowered) && isASCIIAlnum(lowered[end]) {
					continue
				}
			case 4:
				// Reproduce "(?<![0-9])" before the match and
				// "(?![0-9])" after the day component.
				if start > 0 && isDigit(lowered[start-1]) {
					continue
				}
				if end < len(lowered) && isDigit(lowered[end]) {
					continue
				}
			}
			var year int
			if _, err := fmt.Sscanf(lowered[m[2]:m[3]], "%d", &year); err == nil {
				years[year] = true
			}
		}
	}
	return years
}

// relativeCueRe matches the phrases that license resolving dates against
// the system date fact: "tomorrow", "next week", weekday names, ...
var relativeCueRe = regexp.MustCompile(`(?i)\b(today|tonight|tomorrow|yesterday|next|this|coming|now|in \d+ (?:days?|weeks?|months?|years?)|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

// relativeCue reports whether the text reasons about relative time.
func relativeCue(text string) bool {
	return text != "" && relativeCueRe.MatchString(text)
}

// dateKeyRe matches a "date:" fact that is not part of a larger word.
// RE2 has no look-behind, so the leading character is captured instead.
var dateKeyRe = regexp.MustCompile(`(^|[^A-Za-z])date:`)

var isoStampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// withDateFact prefixes the local date fact unless the caller already
// supplied one. A system text that starts with "{" (a JSON blob) is left
// untouched as well.
func withDateFact(system string, autoDate bool) string {
	if !autoDate {
		return system
	}
	if dateKeyRe.MatchString(system) || isoStampRe.MatchString(system) {
		return system
	}
	now := time.Now()
	fact := "date: " + now.Format("2006-01-02") + " " + now.Format("Mon") + " " + now.Format("15:04")
	if strings.TrimSpace(system) == "" {
		return fact
	}
	if strings.HasPrefix(strings.TrimLeft(system, " \t\r\n"), "{") {
		return system
	}
	return fact + "; " + system
}

// licensedYears merges the years seen so far in the conversation with the
// years mentioned in the system facts - but only when the input reasons
// relatively (tomorrow, next week, ...), because an absolute date in the
// input must stand on its own.
func licensedYears(seen map[int]bool, system string, relative bool) map[int]bool {
	out := make(map[int]bool, len(seen))
	for y := range seen {
		out[y] = true
	}
	if len(out) > 0 && system != "" && relative {
		for y := range sourceYears(system) {
			out[y] = true
		}
	}
	return out
}

// resolveRef follows a JSON-Schema "$ref" pointer inside root, with
// ~0/~1 unescaping and a cycle guard.
func resolveRef(node any, root map[string]any) any {
	seen := map[string]bool{}
	for {
		m, ok := node.(map[string]any)
		if !ok {
			return node
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return node
		}
		if seen[ref] {
			return node
		}
		seen[ref] = true
		target := any(root)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			if tm, ok := target.(map[string]any); ok {
				if v, exists := tm[part]; exists {
					target = v
				} else {
					target = map[string]any{}
				}
			} else {
				target = map[string]any{}
			}
		}
		node = target
	}
}

// schemaParametersOf extracts the parameters object of a tool schema,
// accepting a bare parameters object as-is.
func schemaParametersOf(schema any) map[string]any {
	raw, ok := schema.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	if params, ok := raw["parameters"].(map[string]any); ok {
		return params
	}
	return raw
}

// walkGrounding walks emitted arguments against their schema and checks
// every date-typed value against the licensed years. It returns the set
// of checked paths and the set of failing paths.
func walkGrounding(schema, arguments map[string]any, years map[int]bool) (checked, failures map[string]bool) {
	checked, failures = map[string]bool{}, map[string]bool{}
	root := schema
	var walk func(value any, node any, path string)
	walk = func(value any, node any, path string) {
		node = resolveRef(node, root)
		if m, ok := node.(map[string]any); ok {
			variants, _ := m["anyOf"].([]any)
			if variants == nil {
				variants, _ = m["oneOf"].([]any)
			}
			var concrete []any
			for _, v := range variants {
				vm, _ := resolveRef(v, root).(map[string]any)
				if vm == nil || vm["type"] != "null" {
					concrete = append(concrete, v)
				}
			}
			if len(concrete) == 1 {
				node = resolveRef(concrete[0], root)
			}
		}
		m, _ := node.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		if format, _ := m["format"].(string); (format == "date" || format == "date-time") && value != nil {
			if s, ok := value.(string); ok {
				if match := yearSuffix.FindStringSubmatch(s); match != nil && len(years) > 0 {
					var year int
					if _, err := fmt.Sscanf(match[1], "%d", &year); err == nil {
						checked[path] = true
						if !years[year] {
							failures[path] = true
						}
					}
				}
			}
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			properties, _ := m["properties"].(map[string]any)
			for key, item := range typed {
				if propSchema, ok := properties[key]; ok {
					child := key
					if path != "" {
						child = path + "." + key
					}
					walk(item, propSchema, child)
				}
			}
		case []any:
			if items, ok := m["items"]; ok {
				for index, item := range typed {
					walk(item, items, fmt.Sprintf("%s[%d]", path, index))
				}
			}
		}
	}
	walk(arguments, root, "")
	return checked, failures
}

// ungroundedPaths groups the engine-reported ungrounded entries by tool.
func ungroundedPaths(r *Response) map[string]map[string]bool {
	grouped := map[string]map[string]bool{}
	if r == nil || r.Validation == nil {
		return grouped
	}
	for _, name := range r.Validation.Ungrounded {
		tool, _, path := splitToolPath(name)
		if path == "" {
			path = tool
		}
		if grouped[tool] == nil {
			grouped[tool] = map[string]bool{}
		}
		grouped[tool][path] = true
	}
	return grouped
}

// annotateUngrounded folds temporal grounding failures into the response's
// validation report, so callers see one unified "ungrounded" list.
func annotateUngrounded(r *Response, toolSchemas []map[string]any, seenYears map[int]bool, system, text string) {
	if r == nil || len(r.FunctionCalls) == 0 {
		return
	}
	relative := true
	if text != "" {
		relative = relativeCue(text)
	}
	years := licensedYears(seenYears, system, relative)
	if len(years) == 0 {
		return
	}
	schemas := map[string]map[string]any{}
	for _, entry := range toolSchemas {
		if name, ok := entry["name"].(string); ok {
			schemas[name] = entry
		}
	}
	var found []string
	for _, call := range r.FunctionCalls {
		schema := schemas[call.Name]
		if schema == nil {
			continue
		}
		_, failures := walkGrounding(schemaParametersOf(schema), call.Arguments, years)
		var paths []string
		for p := range failures {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			found = append(found, call.Name+"."+p)
		}
	}
	if len(found) == 0 {
		return
	}
	if r.Validation == nil {
		r.Validation = &Validation{}
	}
	existing := map[string]bool{}
	for _, name := range r.Validation.Ungrounded {
		existing[name] = true
	}
	for _, name := range found {
		if !existing[name] {
			r.Validation.Ungrounded = append(r.Validation.Ungrounded, name)
			existing[name] = true
		}
	}
}

// numberTokenRe extracts numeric literals: thousands-separated integers,
// plain integers and decimals. RE2 has no look-behind, so the leading
// boundary character is captured in group 1 instead of being asserted.
var numberTokenRe = regexp.MustCompile(`(?:^|[^\w.,])([-+]?\d{1,3}(?:,\d{3})+(?:\.\d+)?|[-+]?\d+(?:\.\d+)?)`)

// dateFactRe matches the date fact itself, which is stripped from the
// sources before number extraction so "2026" from "date: 2026-07-21"
// does not ground an argument's 2026.
var dateFactRe = regexp.MustCompile(`date:\s*\d{4}-\d{2}-\d{2}(?:\s+[A-Za-z]{3})?(?:\s+\d{2}:\d{2})?|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?`)

// sourceNumbers returns the exact numeric values written in the source
// texts, with date facts removed and thousands separators normalized. The
// set is keyed by the canonical rational form (RatString), so equal values
// from different parses compare equal - a value equality, like the
// fractions the Python reference compares.
func sourceNumbers(sources ...string) map[string]bool {
	var kept []string
	for _, source := range sources {
		if source != "" {
			kept = append(kept, dateFactRe.ReplaceAllString(source, " "))
		}
	}
	text := strings.Join(kept, "\n")
	numbers := map[string]bool{}
	for _, m := range numberTokenRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 || m[1] == "" {
			continue
		}
		if rat, ok := new(big.Rat).SetString(strings.ReplaceAll(m[1], ",", "")); ok {
			numbers[rat.RatString()] = true
		}
	}
	return numbers
}

// groundedNumberPaths returns the paths of numeric argument leaves whose
// value literally occurs in the source texts - an engine-flagged number
// with a source is grounded, only genuinely unsourced numbers count.
func groundedNumberPaths(arguments map[string]any, sources ...string) map[string]bool {
	numbers := sourceNumbers(sources...)
	grounded := map[string]bool{}
	if len(numbers) == 0 {
		return grounded
	}
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case bool:
			return
		case float64:
			if rat, ok := new(big.Rat).SetString(formatNumber(typed)); ok && numbers[rat.RatString()] {
				grounded[path] = true
			}
		case map[string]any:
			for key, item := range typed {
				child := key
				if path != "" {
					child = path + "." + key
				}
				walk(item, child)
			}
		case []any:
			for index, item := range typed {
				walk(item, fmt.Sprintf("%s[%d]", path, index))
			}
		}
	}
	walk(arguments, "")
	return grounded
}

// formatNumber renders a JSON number exactly as it round-trips, without
// an exponent tail that big.Rat would reject.
func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// validateExtraction enforces strict extraction: temporal values whose
// year is not licensed, engine-reported ungrounded paths (except numbers
// with a source), and negated requests all become ExtractionValidationError.
func validateExtraction(text string, schema, arguments map[string]any, r *Response, system string) error {
	checked, failures := walkGrounding(schema, arguments, licensedYears(sourceYears(text), system, relativeCue(text)))
	if r != nil && r.Validation != nil {
		flagged := r.Validation.Ungrounded
		var grounded map[string]bool
		if len(flagged) > 0 {
			grounded = groundedNumberPaths(arguments, text, system)
		}
		for _, name := range flagged {
			path := name
			if idx := strings.Index(name, "."); idx >= 0 {
				path = name[idx+1:]
			}
			if grounded[path] {
				continue
			}
			if !checked[path] || failures[path] {
				failures[path] = true
			}
		}
		if r.Validation.Negation {
			failures["negated request"] = true
		}
	}
	if len(failures) == 0 {
		return nil
	}
	var paths []string
	for p := range failures {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return fmt.Errorf("%w: %s", ExtractionValidationError, strings.Join(paths, ", "))
}
