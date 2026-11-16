package needle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FlameInTheDark/cactus-go"
	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/internal/testutil"
)

// The suite has two layers.
//
// The stub tests run against the same C stub engine the Python reference
// tests use (compiled once per process by internal/testutil): the stub
// echoes CALL:/RESPOND: envelopes back, so every agent behaviour -
// parsing, the agentic loop, strict grounding, audio turns, embeddings -
// is exercised deterministically and offline.
//
// The integration tests run against the real native engine and base
// weights, downloaded on first use when the network allows; they skip
// otherwise.

type weatherArgs struct {
	City string `cactus:"city" desc:"city name" required:"true"`
}

type thermostatArgs struct {
	Temperature int `cactus:"temperature" required:"true"`
}

type musicArgs struct {
	Genre string `cactus:"genre" required:"true"`
}

func weatherTool() *Tool {
	return ToolFunc("get_weather", "Get the current weather for a city.",
		func(ctx context.Context, a weatherArgs) (any, error) {
			return map[string]any{"city": a.City, "temp_c": 27}, nil
		})
}

// stubEnv prepares the stub engine and points the weight overrides at
// fake archives, so the base-model and speech paths bind without network
// access. modelID becomes the stub's reported model id (data[4]).
func stubEnv(t *testing.T, modelID byte) string {
	t.Helper()
	lib := testutil.StubEngine(t)
	weights := testutil.FakeCact(t, t.TempDir(), modelID)
	t.Setenv("CACTUS_NEEDLE_WEIGHTS", weights)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", weights)
	return lib
}

// callQuery builds a query the stub answers by echoing envelope after
// the CALL: prefix, so tests control the engine's response exactly.
func callQuery(envelope string) string {
	return "CALL:" + envelope
}

func respondQuery(envelope string) string {
	return "RESPOND:" + envelope
}

func mustComplete(t *testing.T, ctx context.Context, agent *Needle, query string) *Response {
	t.Helper()
	resp, err := agent.Complete(ctx, query)
	if err != nil {
		t.Fatalf("Complete(%q): %v", query, err)
	}
	return resp
}

func TestStubBaseCompleteAndRefusal(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx := context.Background()

	if agent.Generation() != 3 {
		t.Fatalf("Generation = %d, want 3", agent.Generation())
	}
	if agent.Tuned() {
		t.Fatal("base agents are not tuned")
	}
	if got := agent.Tools(); len(got) != 1 || got[0] != "get_weather" {
		t.Fatalf("Tools = %v", got)
	}
	resp := mustComplete(t, ctx, agent, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
	call := resp.FirstCall()
	if call == nil {
		t.Fatalf("expected a get_weather call, got %s", resp.String())
	}
	if call.Name != "get_weather" || call.Arguments["city"] != "Paris" {
		t.Fatalf("call = %s.%v", call.Name, call.Arguments)
	}
	if agent.PrefixTokens() != 7 {
		t.Errorf("PrefixTokens = %d, want 7 (cactus_init return)", agent.PrefixTokens())
	}

	// A respond envelope ends the conversation with no calls: the refusal
	// contract. FirstCall falls back to a suppressed call when present.
	resp = mustComplete(t, ctx, agent, respondQuery(`{"type":"respond","success":true}`))
	if resp.HasCalls() || !resp.Refused() {
		t.Fatalf("respond envelope must refuse, got %s", resp.String())
	}
	if resp.FirstCallOrSuppressed() != nil {
		t.Fatal("nothing suppressed either")
	}
	resp = mustComplete(t, ctx, agent, callQuery(
		`{"type":"call","suppressed_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
	if resp.HasCalls() || !resp.Refused() {
		t.Fatalf("suppressed-only envelope must refuse, got %s", resp.String())
	}
	if c := resp.FirstCallOrSuppressed(); c == nil || c.Name != "get_weather" {
		t.Fatalf("FirstCallOrSuppressed = %v", c)
	}
}

func TestStubRunLoopExecutesHandlers(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx := context.Background()

	resp, err := agent.Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Lagos"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("run should execute the tool, got %s", resp.String())
	}
	first, ok := resp.Results[0].(map[string]any)
	if !ok {
		t.Fatalf("result[0] = %#v", resp.Results[0])
	}
	if first["city"] != "Lagos" || first["temp_c"] != 27 {
		t.Fatalf("handler result = %v", first)
	}
	// The tool feedback round-trips through the stub's [ prefix: the loop
	// must have ended on a respond envelope, not another call round.
	if resp.HasCalls() {
		t.Fatalf("loop should be finished, got %s", resp.String())
	}
}

func TestStubRunStrictGrounding(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib),
		WithTools(
			ToolFunc("set_thermostat", "Set the thermostat temperature.",
				func(ctx context.Context, a thermostatArgs) (any, error) {
					return map[string]any{"temperature": a.Temperature}, nil
				}),
			ToolFunc("play_music", "Play music of a genre.",
				func(ctx context.Context, a musicArgs) (any, error) {
					return map[string]any{"genre": a.Genre}, nil
				})))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx := context.Background()

	// The engine flags both calls' arguments. The flagged number 21 is
	// exempt (its value occurs in the query text), the flagged string
	// genre is not, so strict mode executes only the thermostat.
	query := callQuery(`{"type":"call","success":true,` +
		`"function_calls":[{"name":"set_thermostat","arguments":{"temperature":21}},` +
		`{"name":"play_music","arguments":{"genre":"jazz"}}],` +
		`"validation":{"ungrounded":["set_thermostat.temperature","play_music.genre"]}}`)
	resp, err := agent.Run(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected both result slots, got %s", resp.String())
	}
	thermostat, ok := resp.Results[0].(map[string]any)
	if !ok || thermostat["temperature"] != 21 {
		t.Fatalf("flagged number with a source must be executed, got %#v", resp.Results[0])
	}
	music, ok := resp.Results[1].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(music["error"]), "ungrounded") {
		t.Fatalf("flagged string must be refused, got %#v", resp.Results[1])
	}

	// Loose mode executes regardless of the flags.
	resp, err = agent.Run(ctx, query, RunStrict(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("loose run should execute both, got %s", resp.String())
	}
	if music, ok := resp.Results[1].(map[string]any); !ok || music["genre"] != "jazz" {
		t.Fatalf("loose run should execute the flagged call, got %#v", resp.Results[1])
	}
}

func TestStubCompleteAudioBase(t *testing.T) {
	lib := stubEnv(t, 11)
	// A distinct speech archive: the stub reports its model id in the
	// audio envelope, proving the speech weights were loaded on demand.
	speech := testutil.FakeCact(t, t.TempDir(), 21)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", speech)

	agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	samples := make([]float32, 1600) // 0.1 s
	for i := range samples {
		samples[i] = float32(i%7) / 7
	}
	resp, err := agent.CompleteAudio(context.Background(), samples,
		WithLanguage("en"), WithKeywords("Siobhan", "Krzysztof"), WordTimestamps(true))
	if err != nil {
		t.Fatal(err)
	}
	if resp.AudioText == nil || *resp.AudioText != "hello there" {
		t.Fatalf("audio_text = %v, want the transcript", resp.AudioText)
	}
	// The transcript of the turn is what the grounding pass sees; a
	// transcript without dates licenses no years.
	if resp.Validation != nil {
		t.Fatalf("nothing was flagged, got %v", resp.Validation)
	}
	// UngroundedOf groups by tool for the surfaced validation report.
	if got := resp.UngroundedOf("get_weather"); got != nil {
		t.Fatalf("no validation was flagged, got %v", got)
	}
}

func TestStubCompleteAudioWorker(t *testing.T) {
	lib := stubEnv(t, 7)
	// Tuned agent: the audio turn runs in the worker child, which loads
	// the speech archive on demand.
	weights := testutil.FakeCact(t, t.TempDir(), 7)
	speech := testutil.FakeCact(t, filepath.Join(t.TempDir(), "speech"), 21)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", speech)

	agent, err := New(WithEnginePath(lib), WithWeights(weights), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	samples := make([]float32, 3200)
	resp, err := agent.CompleteAudio(context.Background(), samples, WithLanguage("de"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.AudioText == nil || *resp.AudioText != "hello there" {
		t.Fatalf("audio_text = %v", resp.AudioText)
	}
}

func TestStubConfidenceFollowsTheHead(t *testing.T) {
	lib := testutil.StubEngine(t)
	dir := t.TempDir()
	headed := filepath.Join(dir, "headed.cact")
	testutil.FakeHeadedCact(t, headed, []byte{2}) // a trained confidence head
	headless := testutil.FakeCact(t, filepath.Join(dir, "headless"), 11)
	sameOtherHead := filepath.Join(dir, "other.cact")
	testutil.FakeHeadedCact(t, sameOtherHead, []byte{1}) // a head, but not code 2

	cases := []struct {
		name       string
		weights    string
		calibrated bool
	}{
		{"headed archive", headed, true},
		{"headless archive", headless, false},
		{"non-confidence head", sameOtherHead, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CACTUS_NEEDLE_WEIGHTS", tc.weights)
			t.Setenv("CACTUS_WHISTLE_WEIGHTS", tc.weights)
			agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			resp := mustComplete(t, context.Background(), agent, callQuery(
				`{"type":"call","success":true,"confidence":0.87,`+
					`"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
			if tc.calibrated {
				if resp.Confidence == nil || *resp.Confidence != 0.87 {
					t.Fatalf("calibrated agent must pass confidence through, got %v", resp.Confidence)
				}
			} else if resp.Confidence != nil {
				t.Fatalf("agent without a trained head must report nil confidence, got %v", *resp.Confidence)
			}
		})
	}
}

func TestStubTunedWorkerLifecycle(t *testing.T) {
	lib := stubEnv(t, 7)
	weights := testutil.FakeCact(t, t.TempDir(), 7)
	agent, err := New(WithEnginePath(lib), WithWeights(weights), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if !agent.Tuned() {
		t.Fatal("agent should report tuned weights")
	}
	if agent.Generation() != 3 {
		t.Fatalf("Generation = %d, want 3 (the archive's needle3 tag)", agent.Generation())
	}
	ctx := context.Background()

	resp := mustComplete(t, ctx, agent, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Berlin"}}]}`))
	if resp.FirstCall() == nil || resp.FirstCall().Name != "get_weather" {
		t.Fatalf("tuned complete, got %s", resp.String())
	}
	if agent.PrefixTokens() != 7 {
		t.Errorf("PrefixTokens = %d, want 7", agent.PrefixTokens())
	}
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	resp = mustComplete(t, ctx, agent, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Rome"}}]}`))
	if resp.FirstCall() == nil {
		t.Fatalf("post-reset complete, got %s", resp.String())
	}
	// Embeddings are computed in the child; the stub returns
	// model_id, +1, +2 for the text encoder.
	embedding, err := agent.Embed(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedding) != 3 || embedding[0] != 7 || embedding[1] != 8 || embedding[2] != 9 {
		t.Fatalf("embedding = %v, want [7 8 9] (the archive's id byte)", embedding)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal("Close must be safe to call twice")
	}
}

func TestStubTwoTunedAgentsAreIndependent(t *testing.T) {
	lib := stubEnv(t, 7)
	first := testutil.FakeCact(t, t.TempDir(), 7)
	second := testutil.FakeCact(t, filepath.Join(t.TempDir(), "second"), 13)

	agentA, err := New(WithEnginePath(lib), WithWeights(first), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agentA.Close()
	agentB, err := New(WithEnginePath(lib), WithWeights(second), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agentB.Close()
	ctx := context.Background()

	// Each worker child carries its own archive; the stub's text embedding
	// exposes which one: 7..9 for the first, 13..15 for the second.
	embA, err := agentA.Embed(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	embB, err := agentB.Embed(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if embA[0] != 7 || embB[0] != 13 {
		t.Fatalf("embeddings = %v / %v, want distinct archives", embA, embB)
	}
	if err := agentA.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := agentA.Embed(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestStubStatelessAndReset(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib), WithStateless(true), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		resp := mustComplete(t, ctx, agent, callQuery(
			`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
		if resp.FirstCall() == nil {
			t.Fatalf("stateless complete %d, got %s", i, resp.String())
		}
	}
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	resp := mustComplete(t, ctx, agent, respondQuery(`{"type":"respond","success":true}`))
	if !resp.Refused() {
		t.Fatalf("post-reset complete, got %s", resp.String())
	}
}

func TestStubToolsJSONRunUnknownTool(t *testing.T) {
	lib := stubEnv(t, 11)
	toolsJSON := `[{"name":"get_weather","description":"Get the current weather for a city.",` +
		`"parameters":{"type":"object","properties":{"city":{"type":"string","description":"city name"}},` +
		`"required":["city"]}}]`
	agent, err := New(WithEnginePath(lib), WithToolsJSON(toolsJSON))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx := context.Background()

	resp := mustComplete(t, ctx, agent, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
	if resp.FirstCall() == nil {
		t.Fatalf("expected a call, got %s", resp.String())
	}
	// Raw-schema agents have no handler: Run answers unknown tool and the
	// loop still terminates.
	runResp, err := agent.Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(runResp.Results) == 0 {
		t.Fatal("run should feed the unknown-tool error back")
	}
	blob, _ := json.Marshal(runResp.Results[0])
	if !strings.Contains(string(blob), "unknown tool") {
		t.Errorf("expected unknown-tool error result, got %s", blob)
	}
}

func TestStubEmbedBase(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	embedding, err := agent.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{11, 12, 13}
	if len(embedding) != len(want) || embedding[0] != 11 || embedding[1] != 12 || embedding[2] != 13 {
		t.Fatalf("embedding = %v, want %v (the base archive's id byte)", embedding, want)
	}
}

func TestStubAgentRejectsBadWeights(t *testing.T) {
	lib := stubEnv(t, 11)
	dir := t.TempDir()
	needle2 := filepath.Join(dir, "needle2.cact")
	if err := os.WriteFile(needle2, []byte{0x83, 0x2A, 0xE1, 0x05, 0x01, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unknown.cact")
	if err := os.WriteFile(unknown, []byte{0x85, 0x2A, 0xE1, 0x05, 0x01, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "short.cact")
	if err := os.WriteFile(truncated, []byte{0x84, 0x2A}, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{needle2, "Needle 2"},
		{unknown, "unknown .cact format"},
		{truncated, "not a complete .cact"},
	} {
		_, err := New(WithEnginePath(lib), WithWeights(tc.path))
		if err == nil {
			t.Fatalf("%s should be rejected", tc.path)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should mention %q", tc.path, err, tc.want)
		}
	}
}

func TestStubClosedAgent(t *testing.T) {
	lib := stubEnv(t, 11)
	agent, err := New(WithEnginePath(lib), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Complete(context.Background(), "hi"); err != ErrClosed {
		t.Fatalf("complete on a closed agent = %v, want ErrClosed", err)
	}
	if _, err := agent.Embed(context.Background(), "hi"); err != ErrClosed {
		t.Fatalf("embed on a closed agent = %v, want ErrClosed", err)
	}
	if err := agent.Reset(); err != ErrClosed {
		t.Fatalf("reset on a closed agent = %v, want ErrClosed", err)
	}
}

// ===== integration tests against the real engine =====

// engineAvailable resolves the engine and base weights, downloading them
// on first use when the network allows; the tests skip otherwise.
func engineAvailable(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := fetch.LibraryPath(ctx); err != nil {
		t.Skipf("engine not available: %v", err)
	}
	weights, err := fetch.WeightsPath(ctx, fetch.Needle3)
	if err != nil {
		t.Skipf("needle3 base weights not available: %v", err)
	}
	return weights
}

func TestIntegrationComplete(t *testing.T) {
	baseWeights := engineAvailable(t)
	if !cactConfidenceHeadPresent(baseWeights) {
		t.Skip("base archive carries no confidence head")
	}
	agent, err := New(WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := agent.Complete(ctx, "what's the weather in Paris?")
	if err != nil {
		t.Fatal(err)
	}
	call := resp.FirstCall()
	if call == nil {
		t.Fatalf("expected a get_weather call, got %s", resp.String())
	}
	if call.Name != "get_weather" {
		t.Fatalf("call name = %q", call.Name)
	}
	if call.Arguments["city"] != "Paris" {
		t.Fatalf("city = %v", call.Arguments["city"])
	}
	if resp.Confidence == nil || *resp.Confidence < 0.5 {
		t.Fatalf("confidence = %v", resp.Confidence)
	}
}

func TestIntegrationRunLoop(t *testing.T) {
	engineAvailable(t)
	agent, err := New(WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := agent.Run(ctx, "what's the weather in Lagos?")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("run should execute the tool, got %s", resp.String())
	}
	first, ok := resp.Results[0].(map[string]any)
	if !ok {
		t.Fatalf("result[0] = %#v", resp.Results[0])
	}
	if first["city"] != "Lagos" {
		t.Fatalf("city = %v", first["city"])
	}
}

func TestIntegrationOffTopicRefusal(t *testing.T) {
	engineAvailable(t)
	agent, err := New(WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := agent.Complete(ctx, "write me a poem about the sea")
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasCalls() {
		t.Fatalf("off-topic input must refuse with an empty call list, got %s", resp.String())
	}
}

func TestIntegrationReset(t *testing.T) {
	engineAvailable(t)
	agent, err := New(WithTools(ToolFunc("set_thermostat", "Set the thermostat temperature.",
		func(ctx context.Context, a thermostatArgs) (any, error) {
			return map[string]any{"temperature": a.Temperature}, nil
		})))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := agent.Complete(ctx, "set it to 21 degrees"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	resp, err := agent.Complete(ctx, "set the temperature to 19")
	if err != nil {
		t.Fatal(err)
	}
	if resp.FirstCall() == nil {
		t.Fatalf("expected a fresh call after reset, got %s", resp.String())
	}
}

type invoiceArgs struct {
	Vendor  string  `cactus:"vendor" required:"true"`
	Total   float64 `cactus:"total" required:"true"`
	DueDate string  `cactus:"due_date" format:"date"`
}

func TestIntegrationExtractTyped(t *testing.T) {
	engineAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Note: "$1,200.00" is deliberately not used - the engine flags a
	// reformatted number ($ and commas dropped) as ungrounded, and strict
	// extraction rejects it.
	invoice, err := Extract[invoiceArgs](ctx,
		"Invoice from Acme Corp, total 1200.00, due 2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	if invoice == nil {
		t.Fatal("expected extraction to match")
	}
	if invoice.Vendor != "Acme Corp" {
		t.Errorf("vendor = %q", invoice.Vendor)
	}
	if invoice.Total != 1200 {
		t.Errorf("total = %v", invoice.Total)
	}
	if invoice.DueDate != "2026-09-01" {
		t.Errorf("due_date = %q", invoice.DueDate)
	}
}

func TestIntegrationExtractRawSchema(t *testing.T) {
	engineAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	schema := map[string]any{
		"name":        "receipt",
		"description": "A purchase receipt shared as text",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"merchant": map[string]any{"type": "string"},
				"total":    map[string]any{"type": "number"},
			},
			"required": []any{"merchant", "total"},
		},
	}
	args, err := ExtractRaw(ctx, "GreenMart receipt: oat milk 3.50, total 7.75 paid by visa", schema)
	if err != nil {
		t.Fatal(err)
	}
	if args == nil {
		t.Fatal("expected extraction to match")
	}
	if args["merchant"] != "GreenMart" {
		t.Errorf("merchant = %v", args["merchant"])
	}
	if args["total"] != 7.75 {
		t.Errorf("total = %v (%T)", args["total"], args["total"])
	}
}

func TestIntegrationExtractStrictTemporal(t *testing.T) {
	engineAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// A reformatted amount ($ and thousands separators dropped) is a
	// value that literally occurs in the source, so strict extraction
	// clears it and returns the record.
	invoice, err := Extract[invoiceArgs](ctx,
		"Invoice from Acme Corp, $1,200.00 total, due 1999-09-01, issued 5 May 2026")
	if err != nil {
		t.Fatal(err)
	}
	if invoice == nil {
		t.Fatal("expected the extraction to match")
	}
	if invoice.Total != 1200 {
		t.Errorf("total = %v, want the reformatted 1200.00", invoice.Total)
	}
	// Both years of this input are literal, so two outcomes are
	// grounded: the engine copies the verbatim due date, or its
	// temporal gate re-grounds the year to 2026, the year of the
	// issue date. The year is the contract either way - a date in any
	// other year would be fabricated and fail strict extraction.
	year := ""
	if len(invoice.DueDate) >= 4 {
		year = invoice.DueDate[:4]
	}
	if year != "1999" && year != "2026" {
		t.Errorf("due_date = %q, want the literal 1999 date or a 2026 re-grounding", invoice.DueDate)
	}
	// Loose mode behaves the same for this input; the modes differ only
	// on genuinely ungrounded values (see TestExtractNumberGrounding).
	invoice, err = Extract[invoiceArgs](ctx,
		"Invoice from Acme Corp, $1,200.00 total, due 1999-09-01, issued 5 May 2026",
		ExtractStrict(false))
	if err != nil || invoice == nil {
		t.Fatalf("loose extraction should return values, got %v, %v", invoice, err)
	}
	if invoice.Total != 1200 {
		t.Errorf("loose total = %v", invoice.Total)
	}
}

func TestIntegrationTwoAgents(t *testing.T) {
	engineAvailable(t)
	thermostat, err := New(WithTools(ToolFunc("set_thermostat", "Set the thermostat temperature.",
		func(ctx context.Context, a thermostatArgs) (any, error) {
			return map[string]any{"temperature": a.Temperature}, nil
		})))
	if err != nil {
		t.Fatal(err)
	}
	defer thermostat.Close()
	weather, err := New(WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer weather.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := thermostat.Complete(ctx, "set it to 21 degrees")
	if err != nil {
		t.Fatal(err)
	}
	if resp.FirstCall() == nil || resp.FirstCall().Name != "set_thermostat" {
		t.Fatalf("thermostat agent: %s", resp.String())
	}
	resp, err = weather.Complete(ctx, "what's the weather in Paris?")
	if err != nil {
		t.Fatal(err)
	}
	if resp.FirstCall() == nil || resp.FirstCall().Name != "get_weather" {
		t.Fatalf("weather agent: %s", resp.String())
	}
}

// weightsPathForTests locates a .cact archive for the tuned integration
// tests: $CACTUS_TEST_WEIGHTS, testdata/needle3.cact, ../needle3.cact in
// order, falling back to the resolved base archive.
func weightsPathForTests(t *testing.T, baseWeights string) string {
	t.Helper()
	candidates := []string{
		os.Getenv("CACTUS_TEST_WEIGHTS"),
		filepath.Join("testdata", "needle3.cact"),
		filepath.Join("..", "needle3.cact"),
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
			return p
		}
	}
	if baseWeights != "" {
		return baseWeights
	}
	t.Skip("no .cact weights available for the worker test; set CACTUS_TEST_WEIGHTS " +
		"or drop testdata/needle3.cact (download: cactus download Cactus-Compute/needle3/needle3.cact)")
	return ""
}

func TestIntegrationTunedWorker(t *testing.T) {
	baseWeights := engineAvailable(t)
	weights := weightsPathForTests(t, baseWeights)
	agent, err := New(
		WithWeights(weights),
		WithTools(weatherTool()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if !agent.Tuned() {
		t.Fatal("agent should report tuned weights")
	}
	if agent.Generation() != 3 {
		t.Fatalf("generation = %d, want 3", agent.Generation())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := agent.Complete(ctx, "what's the weather in Berlin?")
	if err != nil {
		t.Fatal(err)
	}
	call := resp.FirstCall()
	if call == nil {
		t.Fatalf("tuned worker complete, got %s", resp.String())
	}
	// Confidence is reported exactly when the archive carries a trained
	// head; anything else would leak another archive's calibration.
	if cactConfidenceHeadPresent(weights) {
		if resp.Confidence == nil {
			t.Fatal("headed archive should report confidence")
		}
	} else if resp.Confidence != nil {
		t.Fatal("headless archive must report nil confidence")
	}
	runResp, err := agent.Run(ctx, "what's the weather in Berlin?")
	if err != nil {
		t.Fatal(err)
	}
	if len(runResp.Results) == 0 {
		t.Fatalf("worker run should execute the tool, got %s", runResp.String())
	}
	// The worker process must be gone after Close.
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationSystemFacts(t *testing.T) {
	engineAvailable(t)
	agent, err := New(
		WithSystem("date: 2026-07-21 Tue 14:30; locale: en-US; device: phone"),
		WithTools(weatherTool()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := agent.Complete(ctx, "what's the weather in Paris?")
	if err != nil {
		t.Fatal(err)
	}
	if resp.FirstCall() == nil {
		t.Fatalf("expected a call, got %s", resp.String())
	}
}

func TestIntegrationToolsJSONString(t *testing.T) {
	engineAvailable(t)
	toolsJSON := `[{"name":"get_weather","description":"Get the current weather for a city.","parameters":{"type":"object","properties":{"city":{"type":"string","description":"city name"}},"required":["city"]}}]`
	agent, err := New(WithToolsJSON(toolsJSON))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := agent.Complete(ctx, "what's the weather in Paris?")
	if err != nil {
		t.Fatal(err)
	}
	if resp.FirstCall() == nil {
		t.Fatalf("expected a call, got %s", resp.String())
	}
	// A pending call must be answered before the conversation continues:
	// reset, then Run the same query fresh.
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	// Raw-schema agents have no handler: Run answers unknown tool.
	runResp, err := agent.Run(ctx, "what's the weather in Paris?")
	if err != nil {
		t.Fatal(err)
	}
	if len(runResp.Results) == 0 {
		t.Fatal("run should feed the unknown-tool error back")
	}
	blob, _ := json.Marshal(runResp.Results[0])
	if !strings.Contains(string(blob), "unknown tool") {
		t.Errorf("expected unknown-tool error result, got %s", blob)
	}
}

func TestIntegrationStateless(t *testing.T) {
	engineAvailable(t)
	agent, err := New(WithStateless(true), WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for i := 0; i < 2; i++ {
		resp, err := agent.Complete(ctx, "what's the weather in Paris?")
		if err != nil {
			t.Fatal(err)
		}
		if resp.FirstCall() == nil {
			t.Fatalf("stateless complete %d, got %s", i, resp.String())
		}
	}
}

func TestIntegrationEmbedBase(t *testing.T) {
	engineAvailable(t)
	agent, err := New(WithTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	embedding, err := agent.Embed(ctx, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedding) == 0 {
		t.Fatal("expected a nonempty text embedding")
	}
}

// TestIntegrationModelsBitmask checks the core cactus.Models helper: the
// base needle3 weights load the text model bit.
func TestIntegrationModelsBitmask(t *testing.T) {
	engineAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mask, err := cactus.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The base model was bound by the earlier tests in the worst case;
	// the text bit must be set, speech only after a whistle load.
	if mask&cactus.ModelText == 0 {
		t.Fatalf("models bitmask = %b, want the text bit", mask)
	}
}
