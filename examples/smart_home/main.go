// Command smart_home is a complete smart-home environment example: a
// hand-curated tool surface whose enums, bounds and descriptions map
// cleanly onto constrained decoding, plus a frozen acceptance suite.
// Swap the enum values for the rooms and devices your product exposes
// and keep the shapes: closed sets as enums, bounded numbers, verbatim
// copy for free text, five tools or fewer.
//
// The suite ports the reference harness (cactus/environments/_harness.py)
// verbatim: every case runs in a fresh conversation, calls the engine
// flags as ungrounded or negated count as refusals, and a case passes
// when its call set matches - order-insensitively, case-insensitively.
// The bar is the production contract: at least 90% of cases pass and
// zero critical ones fail. The score measures the shipped model, not
// this port: the tool schemas are serialized byte-identically to the
// Python reference (needle's golden parity test pins it), so both
// implementations land on the same cases on the same machine - and the
// per-run score wobbles a little because the automatic date fact
// changes with the clock.
//
//	go run ./examples/smart_home            # run the acceptance suite
//	go run ./examples/smart_home --query "dim the study lights to 30 percent"
//	go run ./examples/smart_home --min-confidence 0.4   # act only above 0.4
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/FlameInTheDark/cactus-go/needle"
)

// System carries the environment's operating rules, as facts rather than
// instructions.
const system = "Map each explicit supported home action to exactly one declared call; " +
	"never duplicate an action. Do not guess missing targets or values. " +
	"Unsupported, invalid, ambiguous, and negated requests return no call."

// ---- Tool schemas (typed struct-tag declarations) ----

type ControlLightsArgs struct {
	Room       string  `cactus:"room" desc:"The room to control." enum:"kitchen|living_room|bedroom|study" required:"true"`
	Action     string  `cactus:"action" desc:"on, off, or dim." enum:"on|off|dim" required:"true"`
	Brightness *int    `cactus:"brightness_percent" desc:"Brightness from 0 to 100. A dim request with a number must carry it in this same call." min:"0" max:"100"`
	Color      *string `cactus:"color" desc:"The light color; include only when the user names one." enum:"warm white|cool white|red|green|blue"`
}

type SetThermostatArgs struct {
	Temperature int `cactus:"temperature" desc:"Target temperature in degrees Celsius." min:"10" max:"30" required:"true"`
}

type ControlFanArgs struct {
	Room   string  `cactus:"room" desc:"The room whose fan to control." enum:"living_room|bedroom|study" required:"true"`
	Action string  `cactus:"action" desc:"on or off." enum:"on|off" required:"true"`
	Speed  *string `cactus:"speed" desc:"Fan speed; include only when stated." enum:"low|medium|high"`
}

type ControlBlindsArgs struct {
	Room   string `cactus:"room" desc:"The room whose blinds to move." enum:"kitchen|living_room|bedroom|study" required:"true"`
	Action string `cactus:"action" desc:"open or close." enum:"open|close" required:"true"`
}

type StartVacuumArgs struct {
	Action string  `cactus:"action" desc:"start, stop, or dock." enum:"start|stop|dock" required:"true"`
	Room   *string `cactus:"room" desc:"The room to clean; include only when starting a run in a stated room." enum:"kitchen|living_room|bedroom"`
}

func toolsList() []map[string]any {
	var out []map[string]any
	for _, t := range tools() {
		out = append(out, t.Schema())
	}
	return out
}

func tools() []*needle.Tool {
	return []*needle.Tool{
		needle.ToolFunc("control_lights",
			"Turn lights on or off in a room, dim them to a brightness percentage, "+
				"or set their color. A color request means action on. Never use this "+
				"for blinds, fans, or any other device.",
			func(ctx context.Context, a ControlLightsArgs) (any, error) {
				return map[string]any{"ok": true, "room": a.Room, "action": a.Action,
					"brightness_percent": a.Brightness, "color": a.Color}, nil
			}),
		needle.ToolFunc("set_thermostat",
			"Set the home thermostat to a target temperature in degrees Celsius. "+
				"This never controls fans, lights, or any other device.",
			func(ctx context.Context, a SetThermostatArgs) (any, error) {
				return map[string]any{"ok": true, "temperature": a.Temperature}, nil
			}),
		needle.ToolFunc("control_fan",
			"Turn a room fan on or off, optionally at a stated speed. This never "+
				"changes the thermostat.",
			func(ctx context.Context, a ControlFanArgs) (any, error) {
				return map[string]any{"ok": true, "room": a.Room, "action": a.Action,
					"speed": a.Speed}, nil
			}),
		needle.ToolFunc("control_blinds",
			"Open or close the window blinds in one stated room. Never pick the room "+
				"yourself. Never use this for lights or the vacuum.",
			func(ctx context.Context, a ControlBlindsArgs) (any, error) {
				return map[string]any{"ok": true, "room": a.Room, "action": a.Action}, nil
			}),
		needle.ToolFunc("start_robot_vacuum",
			"Start or stop the robot vacuum, or send it back to its dock to charge.",
			func(ctx context.Context, a StartVacuumArgs) (any, error) {
				return map[string]any{"ok": true, "action": a.Action, "room": a.Room}, nil
			}),
	}
}

// ---- Frozen acceptance suite (ported verbatim) ----

// expectation is one case: the query, the exact call set it must produce
// (nil means a refusal: no calls at all), and its category. Critical
// cases (missing targets, negations, out-of-bounds values) fail the
// whole suite when they fail.
type expectation struct {
	query    string
	calls    []map[string]any
	category string
	critical bool
}

func suite() []expectation {
	cases := []expectation{
		// positives: one explicit action, one call
		{"turn on the kitchen lights", calls(call("control_lights", "room", "kitchen", "action", "on")), "positive", false},
		{"switch off the lights in the bedroom", calls(call("control_lights", "room", "bedroom", "action", "off")), "positive", false},
		{"dim the living room lights to 35 percent", calls(call("control_lights", "room", "living_room", "action", "dim", "brightness_percent", float64(35))), "positive", false},
		{"turn on the study lights in warm white", calls(call("control_lights", "room", "study", "action", "on", "color", "warm white")), "positive", false},
		{"put the bedroom lights on in blue", calls(call("control_lights", "room", "bedroom", "action", "on", "color", "blue")), "positive", false},
		{"set the thermostat to 22 degrees", calls(call("set_thermostat", "temperature", float64(22))), "positive", false},
		{"warm the house to 24 degrees", calls(call("set_thermostat", "temperature", float64(24))), "positive", false},
		{"cool the whole home down to 19 degrees", calls(call("set_thermostat", "temperature", float64(19))), "positive", false},
		{"turn on the bedroom fan", calls(call("control_fan", "room", "bedroom", "action", "on")), "positive", false},
		{"switch the study fan off", calls(call("control_fan", "room", "study", "action", "off")), "positive", false},
		{"turn on the living room fan at high speed", calls(call("control_fan", "room", "living_room", "action", "on", "speed", "high")), "positive", false},
		{"run the study fan on low", calls(call("control_fan", "room", "study", "action", "on", "speed", "low")), "positive", false},
		{"open the kitchen blinds", calls(call("control_blinds", "room", "kitchen", "action", "open")), "positive", false},
		{"close the blinds in the living room", calls(call("control_blinds", "room", "living_room", "action", "close")), "positive", false},
		{"open up the bedroom blinds", calls(call("control_blinds", "room", "bedroom", "action", "open")), "positive", false},
		{"start the robot vacuum", calls(call("start_robot_vacuum", "action", "start")), "positive", false},
		{"start vacuuming the bedroom", calls(call("start_robot_vacuum", "action", "start", "room", "bedroom")), "positive", false},
		{"stop the robot vacuum", calls(call("start_robot_vacuum", "action", "stop")), "positive", false},
		// missing targets: the model must refuse rather than guess
		{"turn on the lights", nil, "missing", true},
		{"set the thermostat to something comfortable", nil, "missing", true},
		{"open the blinds", nil, "missing", true},
		{"turn the fan on", nil, "missing", true},
		// unsupported devices and non-actions
		{"lock the back door", nil, "irrelevant", false},
		{"check whether the robot vacuum is charging", nil, "irrelevant", false},
		{"play some jazz in the living room", nil, "irrelevant", false},
		// negations
		{"don't turn on the study lights", nil, "negation", true},
		{"do not close the living room blinds", nil, "negation", true},
		{"never run the vacuum while I am on a call", nil, "negation", true},
		// out-of-bounds values
		{"dim the bedroom lights to 150 percent", nil, "invalid", true},
		{"set the thermostat to 40 degrees", nil, "invalid", true},
		// parallel: two independent actions in one query
		{"turn off the bedroom lights and set the thermostat to 18 degrees",
			calls(
				call("control_lights", "room", "bedroom", "action", "off"),
				call("set_thermostat", "temperature", float64(18)),
			), "parallel", false},
		{"start the vacuum in the kitchen and open the living room blinds",
			calls(
				call("start_robot_vacuum", "action", "start", "room", "kitchen"),
				call("control_blinds", "room", "living_room", "action", "open"),
			), "parallel", false},
	}
	return cases
}

// call builds one expected call: {"name": ..., "arguments": {...}}.
func call(name string, kv ...any) map[string]any {
	arguments := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		arguments[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return map[string]any{"name": name, "arguments": arguments}
}

// calls collects the expected call set of a case.
func calls(calls ...map[string]any) []map[string]any { return calls }

func main() {
	query := flag.String("query", "", "answer one query instead of running the suite")
	minConfidence := flag.Float64("min-confidence", 0.0, "production confidence gate: calls below it count as refusals")
	dumpSchema := flag.Bool("schema", false, "print the tool schemas as JSON and exit")
	flag.Parse()

	if *dumpSchema {
		blob, err := json.MarshalIndent(toolsList(), "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(blob))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	agent, err := needle.New(needle.WithTools(tools()), needle.WithSystem(system))
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	if *query != "" {
		resp, err := agent.Complete(ctx, *query)
		if err != nil {
			log.Fatal(err)
		}
		got := gated(resp, *minConfidence)
		if len(got) > 0 {
			for _, call := range got {
				fmt.Printf("%s %v\n", call.Name, call.Arguments)
			}
		} else {
			fmt.Println("(no call: refused)")
		}
		return
	}

	passed, total, failures, critical := 0, 0, 0, 0
	for _, tc := range suite() {
		total++
		// Each case is an independent conversation: reset keeps the tools
		// loaded but rewinds the history.
		if err := agent.Reset(); err != nil {
			log.Fatal(err)
		}
		resp, err := agent.Complete(ctx, tc.query)
		if err != nil {
			log.Fatal(err)
		}
		got := gated(resp, *minConfidence)
		if sameCalls(got, tc.calls) {
			passed++
			continue
		}
		failures++
		if tc.critical {
			critical++
		}
		fmt.Printf("FAIL [%s] %s\n  want %s\n  got  %s\n  envelope %s\n",
			tc.category, tc.query, mustJSON(tc.calls), mustJSON(callMaps(got)), resp.String())
	}
	fmt.Printf("\n%d/%d passed, %d critical failures (confidence gate %v)\n",
		passed, total, critical, *minConfidence)
	// The production contract: 90% of cases pass and no critical one fails.
	if passed < int(math.Round(0.9*float64(total))) || critical > 0 {
		os.Exit(1)
	}
}

// gated applies the harness gates the reference environments run under:
// calls the engine flags as ungrounded or negated count as refusals, and
// so do calls whose confidence sits below the production threshold.
func gated(resp *needle.Response, minConf float64) []needle.FunctionCall {
	got := resp.FunctionCalls
	if len(got) > 0 && resp.Validation != nil &&
		(len(resp.Validation.Ungrounded) > 0 || resp.Validation.Negation) {
		return nil
	}
	if len(got) > 0 && resp.Confidence != nil && *resp.Confidence < minConf {
		return nil
	}
	return got
}

// sameCalls compares two call sets the way the reference harness does:
// each call is folded (strings case-insensitively, integral floats as
// integers) into a sorted-key canonical form, and the sets match when
// their canonical keys are equal - regardless of call order.
func sameCalls(got []needle.FunctionCall, want []map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	gotKeys := make([]string, len(got))
	for i := range got {
		gotKeys[i] = canonical(map[string]any{
			"name":      got[i].Name,
			"arguments": got[i].Arguments,
		})
	}
	wantKeys := make([]string, len(want))
	for i := range want {
		wantKeys[i] = canonical(want[i])
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)
	for i := range gotKeys {
		if gotKeys[i] != wantKeys[i] {
			return false
		}
	}
	return true
}

// canonical folds one call and renders it with sorted keys.
func canonical(call map[string]any) string {
	blob, err := json.Marshal(fold(call))
	if err != nil {
		return fmt.Sprintf("%v", call)
	}
	return string(blob)
}

// fold makes string comparison case-insensitive and integer-valued
// floats compare equal to integers, mirroring the reference's _fold.
func fold(v any) any {
	switch t := v.(type) {
	case string:
		return strings.ToLower(t)
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = fold(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = fold(val)
		}
		return out
	}
	return v
}

// callMaps converts engine calls to the {"name", "arguments"} shape the
// failure lines print.
func callMaps(calls []needle.FunctionCall) []map[string]any {
	out := make([]map[string]any, len(calls))
	for i := range calls {
		out[i] = map[string]any{"name": calls[i].Name, "arguments": calls[i].Arguments}
	}
	return out
}

func mustJSON(v any) string {
	blob, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(blob)
}
