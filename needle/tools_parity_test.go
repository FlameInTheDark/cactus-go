package needle

import (
	"context"
	"testing"
)

// The golden bytes below are json.dumps(build_schema(...)) of the smart
// home environment of the Python reference (cactus/environments/
// smart_home.py) - the exact tools JSON the reference hands the engine.
// The port must produce identical bytes: the engine renders them into
// the model's prompt, and the tuned model is sensitive to the format.
// Regenerate with scripts/ref_smart_home_tools.py when the reference
// changes.
const refSmartHomeToolsJSON = `[{"name": "control_lights", "description": "Turn lights on or off in a room, dim them to a brightness percentage, or set their color. A color request means action on. Never use this for blinds, fans, or any other device.", "parameters": {"type": "object", "properties": {"room": {"type": "string", "enum": ["kitchen", "living_room", "bedroom", "study"], "description": "The room to control."}, "action": {"type": "string", "enum": ["on", "off", "dim"], "description": "on, off, or dim."}, "brightness_percent": {"type": "integer", "description": "Brightness from 0 to 100. A dim request with a number must carry it in this same call.", "minimum": 0, "maximum": 100}, "color": {"type": "string", "enum": ["warm white", "cool white", "red", "green", "blue"], "description": "The light color; include only when the user names one."}}, "required": ["room", "action"]}}, {"name": "set_thermostat", "description": "Set the home thermostat to a target temperature in degrees Celsius. This never controls fans, lights, or any other device.", "parameters": {"type": "object", "properties": {"temperature": {"type": "integer", "description": "Target temperature in degrees Celsius.", "minimum": 10, "maximum": 30}}, "required": ["temperature"]}}, {"name": "control_fan", "description": "Turn a room fan on or off, optionally at a stated speed. This never changes the thermostat.", "parameters": {"type": "object", "properties": {"room": {"type": "string", "enum": ["living_room", "bedroom", "study"], "description": "The room whose fan to control."}, "action": {"type": "string", "enum": ["on", "off"], "description": "on or off."}, "speed": {"type": "string", "enum": ["low", "medium", "high"], "description": "Fan speed; include only when stated."}}, "required": ["room", "action"]}}, {"name": "control_blinds", "description": "Open or close the window blinds in one stated room. Never pick the room yourself. Never use this for lights or the vacuum.", "parameters": {"type": "object", "properties": {"room": {"type": "string", "enum": ["kitchen", "living_room", "bedroom", "study"], "description": "The room whose blinds to move."}, "action": {"type": "string", "enum": ["open", "close"], "description": "open or close."}}, "required": ["room", "action"]}}, {"name": "start_robot_vacuum", "description": "Start or stop the robot vacuum, or send it back to its dock to charge.", "parameters": {"type": "object", "properties": {"action": {"type": "string", "enum": ["start", "stop", "dock"], "description": "start, stop, or dock."}, "room": {"type": "string", "enum": ["kitchen", "living_room", "bedroom"], "description": "The room to clean; include only when starting a run in a stated room."}}, "required": ["action"]}}]`

// smartHomeTools mirrors the reference environment's five @cactus.tool
// declarations as ToolFunc calls.
func smartHomeTools() []*Tool {
	return []*Tool{
		ToolFunc("control_lights",
			"Turn lights on or off in a room, dim them to a brightness percentage, or set their color. A color request means action on. Never use this for blinds, fans, or any other device.",
			func(ctx context.Context, a smartLightsArgs) (any, error) {
				return map[string]any{"ok": true}, nil
			}),
		ToolFunc("set_thermostat",
			"Set the home thermostat to a target temperature in degrees Celsius. This never controls fans, lights, or any other device.",
			func(ctx context.Context, a smartThermostatArgs) (any, error) {
				return map[string]any{"ok": true}, nil
			}),
		ToolFunc("control_fan",
			"Turn a room fan on or off, optionally at a stated speed. This never changes the thermostat.",
			func(ctx context.Context, a smartFanArgs) (any, error) {
				return map[string]any{"ok": true}, nil
			}),
		ToolFunc("control_blinds",
			"Open or close the window blinds in one stated room. Never pick the room yourself. Never use this for lights or the vacuum.",
			func(ctx context.Context, a smartBlindsArgs) (any, error) {
				return map[string]any{"ok": true}, nil
			}),
		ToolFunc("start_robot_vacuum",
			"Start or stop the robot vacuum, or send it back to its dock to charge.",
			func(ctx context.Context, a smartVacuumArgs) (any, error) {
				return map[string]any{"ok": true}, nil
			}),
	}
}

type smartLightsArgs struct {
	Room       string  `cactus:"room" desc:"The room to control." enum:"kitchen|living_room|bedroom|study" required:"true"`
	Action     string  `cactus:"action" desc:"on, off, or dim." enum:"on|off|dim" required:"true"`
	Brightness *int    `cactus:"brightness_percent" desc:"Brightness from 0 to 100. A dim request with a number must carry it in this same call." min:"0" max:"100"`
	Color      *string `cactus:"color" desc:"The light color; include only when the user names one." enum:"warm white|cool white|red|green|blue"`
}

type smartThermostatArgs struct {
	Temperature int `cactus:"temperature" desc:"Target temperature in degrees Celsius." min:"10" max:"30" required:"true"`
}

type smartFanArgs struct {
	Room   string  `cactus:"room" desc:"The room whose fan to control." enum:"living_room|bedroom|study" required:"true"`
	Action string  `cactus:"action" desc:"on or off." enum:"on|off" required:"true"`
	Speed  *string `cactus:"speed" desc:"Fan speed; include only when stated." enum:"low|medium|high"`
}

type smartBlindsArgs struct {
	Room   string `cactus:"room" desc:"The room whose blinds to move." enum:"kitchen|living_room|bedroom|study" required:"true"`
	Action string `cactus:"action" desc:"open or close." enum:"open|close" required:"true"`
}

type smartVacuumArgs struct {
	Action string  `cactus:"action" desc:"start, stop, or dock." enum:"start|stop|dock" required:"true"`
	Room   *string `cactus:"room" desc:"The room to clean; include only when starting a run in a stated room." enum:"kitchen|living_room|bedroom"`
}

// TestToolsMatchReferenceBytes pins the serialized toolset to the
// reference's bytes: key order, separators, escaping, everything.
func TestToolsMatchReferenceBytes(t *testing.T) {
	schemas, _, err := normalizeTools(smartHomeTools())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := serializeTools(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != refSmartHomeToolsJSON {
		t.Fatalf("serialized tools differ from the reference bytes:\n got: %s\nwant: %s", encoded, refSmartHomeToolsJSON)
	}
}

// TestToolsJSONStringStaysVerbatim locks the WithToolsJSON passthrough:
// a tools string is handed to the engine unchanged, like the reference
// passing a str.
func TestToolsJSONStringStaysVerbatim(t *testing.T) {
	raw := "[{\"name\": \"t\", \"parameters\": {\"type\": \"object\"}}]"
	agent, err := New(WithToolsJSON(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if agent.toolsJSON != raw {
		t.Fatalf("toolsJSON = %q, want the verbatim string", agent.toolsJSON)
	}
}
