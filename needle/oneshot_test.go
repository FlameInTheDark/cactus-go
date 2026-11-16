package needle

import (
	"context"
	"testing"
	"time"

	"github.com/FlameInTheDark/cactus-go/internal/testutil"
)

// Tests for the package-level one-shot calls - Run, Complete, CompleteAudio
// and Embed - and the shared one-shot agent they run on. The stub engine
// echoes CALL:/RESPOND: envelopes, so the responses are controlled
// exactly; the shared-agent checks are white-box because the cache is a
// package implementation detail.

func TestStubPackageRunExecutesHandlers(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()

	resp, err := Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`),
		RunEnginePath(lib), RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Run must execute the handler, got %s", resp.String())
	}
	result, ok := resp.Results[0].(map[string]any)
	if !ok || result["city"] != "Paris" || result["temp_c"] != 27 {
		t.Fatalf("result[0] = %#v, want the weather handler's output", resp.Results[0])
	}

	// The same identity reuses the shared agent, and every one-shot run
	// starts from a fresh conversation (the turn counter stays at 1).
	sharedMu.Lock()
	first := sharedAgent
	sharedMu.Unlock()
	if first == nil {
		t.Fatal("Run must leave a shared one-shot agent")
	}
	if !first.stateless {
		t.Fatal("the shared one-shot agent must be stateless")
	}
	resp, err = Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Rome"}}]}`),
		RunEnginePath(lib), RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("second run must execute too, got %s", resp.String())
	}
	sharedMu.Lock()
	reused := sharedAgent == first
	sharedMu.Unlock()
	if !reused {
		t.Fatal("the same one-shot identity must reuse the shared agent")
	}
	first.mu.Lock()
	turns := first.turns
	first.mu.Unlock()
	if turns != 1 {
		t.Fatalf("one-shot runs must reset the conversation, turns = %d", turns)
	}
}

func TestStubPackageCompleteReturnsCallsUnexecuted(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()
	opts := []CompleteOption{
		CompleteEnginePath(lib),
		CompleteTools(weatherTool()),
	}

	resp, err := Complete(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`),
		opts...)
	if err != nil {
		t.Fatal(err)
	}
	call := resp.FirstCall()
	if call == nil || call.Name != "get_weather" || call.Arguments["city"] != "Paris" {
		t.Fatalf("Complete must return the call, got %s", resp.String())
	}
	if len(resp.Results) != 0 {
		t.Fatalf("Complete must not execute handlers, got results %v", resp.Results)
	}

	// The next one-shot call is a fresh conversation: a refusal envelope
	// answers without any memory of the previous turn.
	resp, err = Complete(ctx, respondQuery(`{"type":"respond","success":true}`), opts...)
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasCalls() {
		t.Fatalf("refusal envelope must refuse, got %s", resp.String())
	}
}

func TestStubPackageCompleteAudio(t *testing.T) {
	lib := stubEnv(t, 11)
	speech := testutil.FakeCact(t, t.TempDir(), 21)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", speech)

	samples := make([]float32, 1600) // 0.1 s
	for i := range samples {
		samples[i] = float32(i%7) / 7
	}
	resp, err := CompleteAudio(context.Background(), samples,
		CompleteEnginePath(lib), CompleteTools(weatherTool()), WithLanguage("en"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.AudioText == nil || *resp.AudioText != "hello there" {
		t.Fatalf("audio_text = %v, want the transcript", resp.AudioText)
	}
}

func TestStubPackageEmbed(t *testing.T) {
	lib := stubEnv(t, 11)

	// Embed needs no tools at all: the zero-tool agent must initialize
	// with an empty tool array (never JSON null).
	vec, err := Embed(context.Background(), "a red umbrella", WithEnginePath(lib))
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 || vec[0] != 11 || vec[1] != 12 || vec[2] != 13 {
		t.Fatalf("embedding = %v, want [11 12 13] (the archive's id byte)", vec)
	}
	sharedMu.Lock()
	agent := sharedAgent
	sharedMu.Unlock()
	if agent == nil {
		t.Fatal("Embed must use the shared one-shot agent")
	}
	agent.mu.Lock()
	toolsJSON := agent.toolsJSON
	agent.mu.Unlock()
	if toolsJSON != "[]" {
		t.Fatalf("zero-tool agent toolsJSON = %q, want []", toolsJSON)
	}
}

func TestStubPackageRunTunedWorker(t *testing.T) {
	lib := stubEnv(t, 7)
	weights := testutil.FakeCact(t, t.TempDir(), 7)
	ctx := context.Background()

	// A one-shot run against a tuned archive goes through the worker
	// child exactly like a tuned agent does.
	resp, err := Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Berlin"}}]}`),
		RunEnginePath(lib), RunWeights(weights), RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("tuned run must execute the handler, got %s", resp.String())
	}
	if result, ok := resp.Results[0].(map[string]any); !ok || result["city"] != "Berlin" {
		t.Fatalf("result[0] = %#v", resp.Results[0])
	}
}

func TestStubSharedOneShotSwapsIdentity(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()
	music := ToolFunc("play_music", "Play a music genre.",
		func(ctx context.Context, a musicArgs) (any, error) {
			return map[string]any{"genre": a.Genre}, nil
		})

	if _, err := Complete(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`),
		CompleteEnginePath(lib), CompleteTools(weatherTool())); err != nil {
		t.Fatal(err)
	}
	sharedMu.Lock()
	first := sharedAgent
	sharedMu.Unlock()

	// A different toolset is a different identity: the previous shared
	// agent is closed and replaced.
	if _, err := Complete(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"play_music","arguments":{"genre":"jazz"}}]}`),
		CompleteEnginePath(lib), CompleteTools(music)); err != nil {
		t.Fatal(err)
	}
	sharedMu.Lock()
	second := sharedAgent
	sharedMu.Unlock()
	if first == second {
		t.Fatal("a new identity must build a new shared agent")
	}
	first.mu.Lock()
	closed := first.closed
	first.mu.Unlock()
	if !closed {
		t.Fatal("the replaced shared agent must be closed")
	}

	// The same identity again keeps the agent it built.
	if _, err := Complete(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"play_music","arguments":{"genre":"blues"}}]}`),
		CompleteEnginePath(lib), CompleteTools(music)); err != nil {
		t.Fatal(err)
	}
	sharedMu.Lock()
	reused := sharedAgent == second
	sharedMu.Unlock()
	if !reused {
		t.Fatal("the same identity must reuse its shared agent")
	}
}

func TestStubPackageRunStrictGrounding(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()

	// Strict mode (the default) refuses to execute a call whose arguments
	// the engine flagged ungrounded, feeding an error back instead.
	resp, err := Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}],`+
			`"validation":{"ungrounded":["get_weather.city"]}}`),
		RunEnginePath(lib), RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("the ungrounded call must produce one error result, got %s", resp.String())
	}
	if result, ok := resp.Results[0].(map[string]any); !ok || result["error"] != "ungrounded city" {
		t.Fatalf("result[0] = %#v, want the ungrounded error", resp.Results[0])
	}
}

func TestStubPackageRunUnknownTool(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()

	// A schema-only toolset (no handlers) answers calls with an
	// "unknown tool" error result, exactly as on an agent.
	resp, err := Run(ctx, callQuery(
		`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`),
		RunEnginePath(lib), RunToolsJSON(`[{"name":"get_weather","description":"Get the weather.",
                        "parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected one result, got %s", resp.String())
	}
	if result, ok := resp.Results[0].(map[string]any); !ok || result["error"] != "unknown tool: get_weather" {
		t.Fatalf("result[0] = %#v, want the unknown-tool error", resp.Results[0])
	}
}

// The one-shot calls work end to end on the real engine and base weights,
// reusing the shared agent across two queries.
func TestIntegrationPackageRunAndEmbed(t *testing.T) {
	engineAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := Run(ctx, "what's the weather in Lagos?", RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("run should execute the tool, got %s", resp.String())
	}
	if first, ok := resp.Results[0].(map[string]any); !ok || first["city"] != "Lagos" {
		t.Fatalf("result[0] = %#v, want the weather of Lagos", resp.Results[0])
	}

	// A second one-shot run answers from a fresh conversation on the same
	// shared agent.
	resp, err = Run(ctx, "what's the weather in Paris?", RunTools(weatherTool()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("second run should execute the tool, got %s", resp.String())
	}

	vec, err := Embed(ctx, "a red umbrella")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) == 0 {
		t.Fatal("embed returned an empty vector")
	}
}

// Concurrent one-shot calls with the same identity serialize on the shared
// agent's mutex; this test exists to run under -race.
func TestStubPackageCallsAreConcurrencySafe(t *testing.T) {
	lib := stubEnv(t, 11)
	ctx := context.Background()

	const callers = 4
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, err := Complete(ctx, callQuery(
				`{"type":"call","success":true,"function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`),
				CompleteEnginePath(lib), CompleteTools(weatherTool()))
			done <- err
		}()
	}
	for i := 0; i < callers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
