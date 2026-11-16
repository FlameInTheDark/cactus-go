package needle

import (
	"log/slog"
)

// Option configures a Needle.
type Option func(*agentConfig)

type agentConfig struct {
	toolsSource   any
	system        string
	weightsPath   string
	toolIndexPath string
	bufSize       int
	enginePath    string
	logger        *slog.Logger
	autoDate      bool
	stateless     bool
}

func newConfig(opts []Option) *agentConfig {
	cfg := &agentConfig{bufSize: 65536, autoDate: true}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return cfg
}

// WithTools declares the agent's toolset. Accepted forms:
//
//   - values built by ToolFunc or NewTool(...).Build() (*Tool)
//   - raw JSON-schema maps ({"name": ..., "description": ...,
//     "parameters": {...}}), or bare parameter objects
//   - a JSON string holding an array of schemas (exactly what the engine
//     consumes)
//   - any slice mixing the above
func WithTools(tools ...any) Option {
	return func(c *agentConfig) { c.toolsSource = tools }
}

// WithToolsJSON declares the toolset from a JSON string: an array of
// {"name", "description", "parameters"} schema objects.
func WithToolsJSON(toolsJSON string) Option {
	return func(c *agentConfig) { c.toolsSource = toolsJSON }
}

// WithSystem attaches environment facts to every conversation turn. Facts,
// never instructions: recognized keys are date, locale, device, battery,
// network, location, user and assistant. Relative language ("tomorrow at
// 7") resolves only when a date: fact licenses it.
//
// Unless the text already carries a date fact, the local date is prefixed
// automatically; pass WithAutoDate(false) to opt out.
func WithSystem(system string) Option {
	return func(c *agentConfig) { c.system = system }
}

// WithAutoDate toggles the automatic "date: <today>" system fact (default
// on). The fact is only prefixed when the system text does not already
// carry one.
func WithAutoDate(autoDate bool) Option {
	return func(c *agentConfig) { c.autoDate = autoDate }
}

// WithWeights runs the agent on a tuned .cact archive instead of the
// base model. The archive's format tag selects a compatible engine
// generation automatically. Each tuned agent runs in its own worker
// process, so several tuned agents with different weights can coexist.
// Confidence is reported only when the archive carries a trained
// confidence head.
func WithWeights(path string) Option {
	return func(c *agentConfig) { c.weightsPath = path }
}

// WithToolIndexPath is reserved for engine tool-index archives.
func WithToolIndexPath(path string) Option {
	return func(c *agentConfig) { c.toolIndexPath = path }
}

// WithBufferSize sizes the response buffer, in bytes (default 65536). A
// response larger than the buffer fails with the engine's truncation
// error.
func WithBufferSize(size int) Option {
	return func(c *agentConfig) { c.bufSize = size }
}

// WithEnginePath pins the native engine library to an explicit path,
// overriding the environment variables and the cache lookup. Use it to
// ship an engine next to your binary.
func WithEnginePath(path string) Option {
	return func(c *agentConfig) { c.enginePath = path }
}

// WithLogger installs a logger for download progress, engine lifecycle
// events and long-session warnings. The agent is silent by default.
func WithLogger(logger *slog.Logger) Option {
	return func(c *agentConfig) { c.logger = logger }
}

// WithStateless resets the agent's conversation before every Complete,
// CompleteAudio and Run call, so each query is answered independently.
func WithStateless(stateless bool) Option {
	return func(c *agentConfig) { c.stateless = stateless }
}

// CompleteOption tunes one completion call.
type CompleteOption func(*completeConfig)

type completeConfig struct {
	maxNewTokens   int
	language       string
	keywords       any
	wordTimestamps bool

	// One-shot identity (package-level Complete and CompleteAudio): the
	// agent these calls run on is built from these fields and shared
	// between calls with the same identity.
	toolsSource any
	system      string
	weightsPath string
	enginePath  string
	logger      *slog.Logger
}

// CompleteTools declares the toolset of the one-shot package-level
// Complete and CompleteAudio calls. Accepted forms are the same as
// WithTools; tools built with ToolFunc or NewTool carry their handlers,
// so the agentic Run can execute them.
func CompleteTools(tools ...any) CompleteOption {
	return func(c *completeConfig) { c.toolsSource = tools }
}

// CompleteToolsJSON declares the one-shot toolset from a JSON string of
// schema objects.
func CompleteToolsJSON(toolsJSON string) CompleteOption {
	return func(c *completeConfig) { c.toolsSource = toolsJSON }
}

// CompleteSystem attaches environment facts to a one-shot completion.
func CompleteSystem(system string) CompleteOption {
	return func(c *completeConfig) { c.system = system }
}

// CompleteWeights runs a one-shot completion against a tuned .cact
// archive.
func CompleteWeights(path string) CompleteOption {
	return func(c *completeConfig) { c.weightsPath = path }
}

// CompleteEnginePath pins the engine library for a one-shot completion.
func CompleteEnginePath(path string) CompleteOption {
	return func(c *completeConfig) { c.enginePath = path }
}

// CompleteLogger installs a logger for a one-shot completion.
func CompleteLogger(logger *slog.Logger) CompleteOption {
	return func(c *completeConfig) { c.logger = logger }
}

// MaxTokens caps the response length in tokens (default 512).
func MaxTokens(n int) CompleteOption {
	return func(c *completeConfig) { c.maxNewTokens = n }
}

// WithLanguage sets the transcription language for CompleteAudio, as an
// ISO code ("en", "de", ...). Empty detects it.
func WithLanguage(language string) CompleteOption {
	return func(c *completeConfig) { c.language = language }
}

// WithKeywords biases CompleteAudio transcription towards the given
// words and phrases (a single string with one phrase per line, or the
// phrases as separate arguments).
func WithKeywords(keywords ...string) CompleteOption {
	return func(c *completeConfig) { c.keywords = keywords }
}

// WordTimestamps asks CompleteAudio for per-word timings.
func WordTimestamps(wordTimestamps bool) CompleteOption {
	return func(c *completeConfig) { c.wordTimestamps = wordTimestamps }
}

// RunOption tunes one agentic loop. The identity options (RunTools,
// RunSystem, ...) apply to the package-level Run; MaxSteps, RunMaxTokens
// and RunStrict apply to every run.
type RunOption func(*runConfig)

type runConfig struct {
	maxSteps     int
	maxNewTokens int
	strict       *bool

	// One-shot identity (package-level Run).
	toolsSource any
	system      string
	weightsPath string
	enginePath  string
	logger      *slog.Logger
}

// RunTools declares the toolset of the one-shot package-level Run.
// Accepted forms are the same as WithTools; tools built with ToolFunc or
// NewTool carry their handlers, which Run executes.
func RunTools(tools ...any) RunOption {
	return func(c *runConfig) { c.toolsSource = tools }
}

// RunToolsJSON declares the one-shot toolset from a JSON string of
// schema objects. Calls against tools without handlers answer with an
// "unknown tool" error result, exactly as on an agent.
func RunToolsJSON(toolsJSON string) RunOption {
	return func(c *runConfig) { c.toolsSource = toolsJSON }
}

// RunSystem attaches environment facts to a one-shot run.
func RunSystem(system string) RunOption {
	return func(c *runConfig) { c.system = system }
}

// RunWeights runs a one-shot run against a tuned .cact archive.
func RunWeights(path string) RunOption {
	return func(c *runConfig) { c.weightsPath = path }
}

// RunEnginePath pins the engine library for a one-shot run.
func RunEnginePath(path string) RunOption {
	return func(c *runConfig) { c.enginePath = path }
}

// RunLogger installs a logger for a one-shot run.
func RunLogger(logger *slog.Logger) RunOption {
	return func(c *runConfig) { c.logger = logger }
}

// MaxSteps caps the number of tool-call rounds in Needle.Run (default 8).
func MaxSteps(n int) RunOption {
	return func(c *runConfig) { c.maxSteps = n }
}

// RunMaxTokens caps each round's response length in tokens (default 512).
func RunMaxTokens(n int) RunOption {
	return func(c *runConfig) { c.maxNewTokens = n }
}

// RunStrict toggles strict grounding inside Needle.Run. Strict (the
// default) refuses to execute a call whose arguments the engine flagged
// ungrounded - except numbers whose value literally occurs in the query
// or system facts - feeding an error back to the model instead; loose
// executes regardless.
func RunStrict(strict bool) RunOption {
	return func(c *runConfig) { c.strict = &strict }
}

// ExtractOption tunes one extraction.
type ExtractOption func(*extractConfig)

type extractConfig struct {
	system       string
	maxNewTokens int
	strict       *bool
	weightsPath  string
	enginePath   string
	logger       *slog.Logger
}

// ExtractSystem attaches system facts to a one-shot extraction.
func ExtractSystem(system string) ExtractOption {
	return func(c *extractConfig) { c.system = system }
}

// ExtractMaxTokens caps the response length in tokens (default 512).
func ExtractMaxTokens(n int) ExtractOption {
	return func(c *extractConfig) { c.maxNewTokens = n }
}

// ExtractStrict toggles strict extraction (default true): temporal values
// that contradict a literal year in the input, engine-reported fabricated
// values, and negated requests raise ExtractionValidationError instead of
// being returned silently. A number the engine flags is exempt when its
// value occurs in the source text.
func ExtractStrict(strict bool) ExtractOption {
	return func(c *extractConfig) { c.strict = &strict }
}

// ExtractWeights extracts against a tuned .cact archive.
func ExtractWeights(path string) ExtractOption {
	return func(c *extractConfig) { c.weightsPath = path }
}

// ExtractEnginePath pins the engine library for a one-shot extraction.
func ExtractEnginePath(path string) ExtractOption {
	return func(c *extractConfig) { c.enginePath = path }
}

// ExtractLogger installs a logger for a one-shot extraction.
func ExtractLogger(logger *slog.Logger) ExtractOption {
	return func(c *extractConfig) { c.logger = logger }
}
