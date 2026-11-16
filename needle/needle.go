// Package needle is the text model of the Cactus stack: Needle 3 tool
// calling, structured extraction and embeddings, running on the same
// shared native engine and the same install as Whistle (speech) and
// Mirage (vision). Cactus is the core - this package is one of its three
// models.
//
// Text goes in, a grammar-constrained JSON tool call comes out:
//
//	agent, err := needle.New(needle.WithTools(sendEmail))
//	resp, err := agent.Run(ctx, "email finance@example.com about expenses")
//	fmt.Println(resp.Results)
//
// Speech goes in, the same grammar-constrained call comes out:
//
//	resp, err := agent.CompleteAudio(ctx, "memo.wav", needle.WithLanguage("en"))
//
// The engine and the base weights are fetched once from the Hugging Face
// Hub on first use and cached under ~/.cache/cactus; inference never
// touches the network. The zero-setup package-level Run, Complete,
// CompleteAudio, Embed and Extract mirror whistle.Transcribe and
// mirage.Describe.
package needle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/FlameInTheDark/cactus-go"
	"github.com/FlameInTheDark/cactus-go/audio"
	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/internal/engine"
)

// unresetTurns is the number of queries after which the agent warns that
// the conversation should have been reset.
const unresetTurns = 4

// Needle binds one toolset, one optional system turn and one optional
// tuned weights archive to a native engine session - the text-model peer
// of whistle.Whistle and mirage.Mirage. A Needle is an agent: it owns a
// conversation, so repeated Complete calls continue it, Reset rewinds it
// while keeping the tools loaded, and Close releases the session. One
// agent per toolset; to change tools, build a new one with New.
type Needle struct {
	mu            sync.Mutex
	system        string
	toolsJSON     string
	toolSchemas   []map[string]any
	handlers      map[string]Handler
	nTools        int
	weightsPath   string
	generation    int
	tuned         bool
	calibrated    bool
	toolIndexPath string
	bufSize       int
	enginePath    string
	logger        *slog.Logger
	stateless     bool

	backend   engine.Backend
	prefix    int
	seenYears map[int]bool
	turns     int
	closed    bool
}

// New creates an agent bound to the Needle 3 base model, or to the tuned
// .cact archive passed with WithWeights. The engine and base weights are
// located (or downloaded) lazily on first use, so New itself only
// validates and normalizes its inputs.
func New(opts ...Option) (*Needle, error) {
	cfg := newConfig(opts)
	a := &Needle{
		system:        withDateFact(cfg.system, cfg.autoDate),
		weightsPath:   cfg.weightsPath,
		generation:    cactus.EngineGeneration,
		toolIndexPath: cfg.toolIndexPath,
		bufSize:       cfg.bufSize,
		enginePath:    cfg.enginePath,
		logger:        cfg.logger,
		stateless:     cfg.stateless,
		seenYears:     map[int]bool{},
	}

	// A tools string (WithToolsJSON) is handed to the engine verbatim -
	// the same passthrough the reference gives a str - after a parse
	// check; everything else is normalized and serialized with the
	// reference's key order, so the prompt bytes match a Python
	// declaration of the same toolset. The plain views feed the
	// grounding passes either way.
	var handlers map[string]Handler
	if raw, ok := cfg.toolsSource.(string); ok && strings.TrimSpace(raw) != "" {
		a.toolsJSON = raw
		parsed, _, err := normalizeTools(raw)
		if err != nil {
			return nil, err
		}
		a.toolSchemas = schemaViews(parsed)
	} else {
		schemas, hs, err := normalizeTools(cfg.toolsSource)
		if err != nil {
			return nil, err
		}
		encoded, err := serializeTools(schemas)
		if err != nil {
			return nil, fmt.Errorf("needle: cannot serialize tool schemas: %w", err)
		}
		a.toolsJSON = encoded
		a.toolSchemas = schemaViews(schemas)
		handlers = hs
	}
	a.handlers = handlers
	a.nTools = len(a.toolSchemas)

	if cfg.weightsPath != "" {
		gen, err := engine.GenerationOfCactFile(cfg.weightsPath)
		if err != nil {
			return nil, err
		}
		a.generation = gen
		a.tuned = true
		// A tuned archive keeps its confidence score only when it carries a
		// confidence head trained for those weights; platform fine-tunes
		// keep it, local builds without a head report nil. The warning is
		// deferred to bind so shared one-shot agents (package Run,
		// Complete, Extract) warn once, not on every call.
		a.calibrated = gen >= 3 && cactConfidenceHeadPresent(cfg.weightsPath)
	}
	return a, nil
}

// Tools returns the declared tool names, in declaration order.
func (a *Needle) Tools() []string {
	names := make([]string, 0, len(a.toolSchemas))
	for _, schema := range a.toolSchemas {
		if name, ok := schema["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// Generation reports the engine generation the agent runs on: 3 for the
// base model, or the generation of its tuned .cact archive.
func (a *Needle) Generation() int { return a.generation }

// Tuned reports whether the agent runs tuned weights.
func (a *Needle) Tuned() bool { return a.tuned }

// PrefixTokens reports the tool-prompt prefix token count observed when
// the engine session was last initialized. It is 0 until the agent binds
// and completes its first turn.
func (a *Needle) PrefixTokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backend == nil {
		return a.prefix
	}
	return a.backend.PrefixTokens()
}

// bind lazily locates the engine and starts the backend.
func (a *Needle) bind(ctx context.Context) error {
	if a.closed {
		return ErrClosed
	}
	if a.backend != nil {
		return nil
	}
	libPath := a.enginePath
	if libPath == "" {
		var err error
		libPath, err = fetch.LibraryPath(ctx)
		if err != nil {
			return err
		}
	}
	if a.logger != nil {
		a.logger.Debug("needle: engine library", "path", libPath, "generation", a.generation)
	}
	if a.tuned {
		worker, err := engine.StartWorker(ctx, libPath, a.weightsPath,
			a.system, a.toolsJSON, a.toolIndexPath, a.bufSize, a.generation, engine.Threads())
		if err != nil {
			return err
		}
		a.backend = worker
		a.prefix = worker.PrefixTokens()
	} else {
		baseWeights, err := fetch.WeightsPath(ctx, fetch.Needle3)
		if err != nil {
			return err
		}
		// The shipped base archive carries a trained confidence head; a
		// foreign or stripped base file without one reports nil, exactly
		// like a tuned archive.
		a.calibrated = cactConfidenceHeadPresent(baseWeights)
		session, err := engine.NewTextSession(libPath, a.system,
			a.toolsJSON, a.toolIndexPath, baseWeights, a.bufSize)
		if err != nil {
			return err
		}
		a.backend = session
	}
	if !a.calibrated && a.logger != nil {
		a.logger.Warn("these weights carry no confidence head trained for them, so this " +
			"agent reports confidence as nil; platform fine-tunes keep the head")
	}
	return nil
}

// countQuery tracks turns for the unreset-conversation warning.
func (a *Needle) countQuery() {
	a.turns++
	if a.turns == unresetTurns+1 && a.logger != nil {
		a.logger.Warn(fmt.Sprintf("%d queries on this agent without Reset: every turn stays in the "+
			"conversation, so unrelated queries lose accuracy and confidence. Call Reset() between "+
			"independent queries, or construct with WithStateless(true)", unresetTurns))
	}
}

// completeLocked performs one text turn; a.mu must be held.
func (a *Needle) completeLocked(ctx context.Context, text string, maxNewTokens int, ground bool) (*Response, error) {
	if err := a.bind(ctx); err != nil {
		return nil, err
	}
	for y := range sourceYears(text) {
		a.seenYears[y] = true
	}
	raw, err := a.backend.Complete(ctx, text, maxNewTokens)
	if err != nil {
		return nil, err
	}
	return a.response(raw, text, ground)
}

// response parses a raw envelope and applies the calibration and
// grounding passes; a.mu must be held.
func (a *Needle) response(raw string, text string, ground bool) (*Response, error) {
	var response Response
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, fmt.Errorf("needle: engine returned an unparseable envelope (%v); "+
			"this is an engine bug - please report it with the prompt and schema", err)
	}
	if !a.calibrated {
		response.Confidence = nil
	}
	if ground {
		annotateUngrounded(&response, a.toolSchemas, a.seenYears, a.system, text)
	}
	return &response, nil
}

// Complete performs one conversation turn and returns the engine's
// response envelope. When the model wants tool calls the response carries
// them (execute and feed results back with the next Complete); when it
// does not, an empty FunctionCalls list is the refusal contract - there
// is no free-text fallback.
func (a *Needle) Complete(ctx context.Context, text string, opts ...CompleteOption) (*Response, error) {
	cfg := completeConfig{maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return a.complete(ctx, text, cfg)
}

func (a *Needle) complete(ctx context.Context, text string, cfg completeConfig) (*Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateless {
		if err := a.resetLocked(ctx); err != nil {
			return nil, err
		}
	}
	a.countQuery()
	return a.completeLocked(ctx, text, cfg.maxNewTokens, true)
}

// CompleteAudio performs one speech-driven turn: the engine transcribes
// the clip and feeds the transcript through the tool-calling grammar in a
// single pass. The response envelope carries the transcript in AudioText
// next to the usual tool-call fields. input is a WAV path, raw float32 LE
// bytes or a sample slice; language is an ISO code or empty to detect it.
func (a *Needle) CompleteAudio(ctx context.Context, input any, opts ...CompleteOption) (*Response, error) {
	cfg := completeConfig{maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return a.completeAudio(ctx, input, cfg)
}

func (a *Needle) completeAudio(ctx context.Context, input any, cfg completeConfig) (*Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateless {
		if err := a.resetLocked(ctx); err != nil {
			return nil, err
		}
	}
	a.countQuery()
	if err := a.bind(ctx); err != nil {
		return nil, err
	}
	samples, err := audio.Samples(input)
	if err != nil {
		return nil, err
	}
	language, keywords, err := audio.Options(cfg.language, cfg.keywords)
	if err != nil {
		return nil, err
	}
	speech, err := fetch.WeightsPath(ctx, fetch.Whistle)
	if err != nil {
		return nil, err
	}
	backend, ok := a.backend.(engine.TextBackend)
	if !ok {
		return nil, fmt.Errorf("needle: this engine backend does not accept audio")
	}
	raw, err := backend.CompleteAudio(ctx, samples, engine.AudioOptions{
		Language:       language,
		Keywords:       keywords,
		WordTimestamps: cfg.wordTimestamps,
		SpeechWeights:  speech,
	}, cfg.maxNewTokens)
	if err != nil {
		return nil, err
	}
	heard := ""
	if raw != "" {
		var peek struct {
			AudioText string `json:"audio_text"`
		}
		if json.Unmarshal([]byte(raw), &peek) == nil {
			heard = peek.AudioText
		}
	}
	for y := range sourceYears(heard) {
		a.seenYears[y] = true
	}
	return a.response(raw, heard, true)
}

// Embed returns the text embedding of the input from the model the agent
// runs on.
func (a *Needle) Embed(ctx context.Context, text string) ([]float32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if err := a.bind(ctx); err != nil {
		return nil, err
	}
	backend, ok := a.backend.(engine.TextBackend)
	if !ok {
		return nil, fmt.Errorf("needle: this engine backend cannot embed text")
	}
	return backend.Embed(ctx, text)
}

// Run drives the full agentic loop: the model picks calls, Run executes
// the declared handlers, feeds each round's results back as the next
// turn, and stops when the model responds without calls or when maxSteps
// rounds have elapsed. The final response carries every executed result,
// flattened, in Results. Strict mode (the default) refuses to execute
// calls whose arguments were flagged ungrounded - except numbers whose
// value literally occurs in the query or system facts - feeding an error
// result back instead.
func (a *Needle) Run(ctx context.Context, query string, opts ...RunOption) (*Response, error) {
	cfg := runConfig{maxSteps: 8, maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return a.run(ctx, query, cfg)
}

func (a *Needle) run(ctx context.Context, query string, cfg runConfig) (*Response, error) {
	strict := true
	if cfg.strict != nil {
		strict = *cfg.strict
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateless {
		if err := a.resetLocked(ctx); err != nil {
			return nil, err
		}
	}
	a.countQuery()

	response, err := a.completeLocked(ctx, query, cfg.maxNewTokens, true)
	if err != nil {
		return nil, err
	}
	var executed []any
	for step := 0; step < cfg.maxSteps; step++ {
		if response.Type != "call" || len(response.FunctionCalls) == 0 {
			break
		}
		ungrounded := ungroundedPaths(response)
		results := make([]any, 0, len(response.FunctionCalls))
		for _, call := range response.FunctionCalls {
			var fabricated []string
			if paths := ungrounded[call.Name]; paths != nil {
				for p := range paths {
					fabricated = append(fabricated, p)
				}
			}
			sortStrings(fabricated)
			if strict && len(fabricated) > 0 {
				// A flagged number is exempt when its value occurs in the
				// query or the system facts, thousands separators aside.
				grounded := groundedNumberPaths(call.Arguments, query, a.system)
				kept := fabricated[:0]
				for _, path := range fabricated {
					if !grounded[path] {
						kept = append(kept, path)
					}
				}
				fabricated = kept
			}
			if strict && len(fabricated) > 0 {
				results = append(results, map[string]any{
					"error": "ungrounded " + joinStrings(fabricated, ", "),
				})
				continue
			}
			handler := a.handlers[call.Name]
			if handler == nil {
				results = append(results, map[string]any{
					"error": "unknown tool: " + call.Name,
				})
				continue
			}
			value, err := safeInvoke(ctx, handler, call.Name, call.Arguments)
			if err != nil {
				results = append(results, map[string]any{"error": err.Error()})
				continue
			}
			results = append(results, jsonable(value))
		}
		executed = append(executed, results...)
		feedback, err := json.Marshal(results)
		if err != nil {
			return nil, fmt.Errorf("needle: cannot serialize tool results: %w", err)
		}
		response, err = a.completeLocked(ctx, string(feedback), cfg.maxNewTokens, false)
		if err != nil {
			return nil, err
		}
	}
	response.Results = executed
	return response, nil
}

// safeInvoke runs a handler with panic recovery.
func safeInvoke(ctx context.Context, handler Handler, name string, arguments map[string]any) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("tool %s panicked: %v", name, r)
		}
	}()
	return handler(ctx, arguments)
}

// jsonable makes a handler result feedable: values that do not marshal
// fall back to their fmt representation.
func jsonable(value any) any {
	if value == nil {
		return nil
	}
	if _, err := json.Marshal(value); err != nil {
		return fmt.Sprintf("%v", value)
	}
	return value
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// Reset rewinds the conversation, keeping the tools loaded.
func (a *Needle) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resetLocked(context.Background())
}

func (a *Needle) resetLocked(ctx context.Context) error {
	if a.closed {
		return ErrClosed
	}
	if err := a.bind(ctx); err != nil {
		return err
	}
	if err := a.backend.Reset(); err != nil {
		return err
	}
	a.seenYears = map[int]bool{}
	a.turns = 0
	return nil
}

// Close releases the agent's engine session. It is safe to call more than
// once and required for tuned agents (their worker process is killed).
func (a *Needle) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.backend != nil {
		return a.backend.Close()
	}
	return nil
}
