package needle

import (
        "context"
        "encoding/json"
        "fmt"
)

// Extract performs a one-shot structured extraction with the schema
// derived from the argument struct's tags. It returns nil (and no error)
// when nothing in the input matched the schema, the extracted fields
// decoded into A otherwise. Strict mode (the default) rejects temporal
// values that contradict a literal year in the input, engine-reported
// fabricated values, and negated requests with ExtractionValidationError.
//
//      type Invoice struct {
//          Vendor  string  `cactus:"vendor" required:"true"`
//          Total   float64 `cactus:"total" required:"true"`
//          DueDate string  `cactus:"due_date" format:"date"`
//      }
//
//      invoice, err := needle.Extract[Invoice](ctx,
//          "Invoice from Acme Corp, $1,200.00, due 2026-09-01")
func Extract[A any](ctx context.Context, text string, opts ...ExtractOption) (*A, error) {
        parameters, err := schemaFromTypeOf[A]()
        if err != nil {
                return nil, err
        }
        cfg := newExtractConfig(opts)
        return extractAs[A](ctx, text, parameters, parameters.view(), cfg)
}

// ExtractAs performs a one-shot extraction against an explicit schema and
// decodes the result into A. The schema may be a *Tool, a raw schema map,
// or a JSON string.
func ExtractAs[A any](ctx context.Context, text string, schema any, opts ...ExtractOption) (*A, error) {
        raw, view, err := extractionParameters(schema)
        if err != nil {
                return nil, err
        }
        cfg := newExtractConfig(opts)
        return extractAs[A](ctx, text, raw, view, cfg)
}

// ExtractRaw performs a one-shot extraction against an explicit schema
// and returns the raw argument map.
func ExtractRaw(ctx context.Context, text string, schema any, opts ...ExtractOption) (map[string]any, error) {
        raw, view, err := extractionParameters(schema)
        if err != nil {
                return nil, err
        }
        cfg := newExtractConfig(opts)
        response, arguments, facts, err := runExtraction(ctx, text, raw, cfg)
        if err != nil {
                return nil, err
        }
        if arguments == nil {
                return nil, nil
        }
        if *cfg.strict {
                if err := validateExtraction(text, view, arguments, response, facts); err != nil {
                        return nil, err
                }
        }
        return arguments, nil
}

// ExtractRaw on an agent reuses the agent's weights, engine path and
// system facts for a one-shot extraction (a fresh engine conversation).
// Use the package-level Extract or ExtractAs for typed results, passing
// ExtractWeights to inherit a tuned archive.
func (a *Needle) ExtractRaw(ctx context.Context, text string, schema any, opts ...ExtractOption) (map[string]any, error) {
        cfg := newExtractConfig(opts)
        if cfg.weightsPath == "" && a.tuned {
                cfg.weightsPath = a.weightsPath
        }
        if cfg.enginePath == "" && a.enginePath != "" {
                cfg.enginePath = a.enginePath
        }
        if cfg.system == "" {
                cfg.system = a.system
        }
        raw, view, err := extractionParameters(schema)
        if err != nil {
                return nil, err
        }
        response, arguments, facts, err := runExtraction(ctx, text, raw, cfg)
        if err != nil {
                return nil, err
        }
        if arguments == nil {
                return nil, nil
        }
        if *cfg.strict {
                if err := validateExtraction(text, view, arguments, response, facts); err != nil {
                        return nil, err
                }
        }
        return arguments, nil
}

func newExtractConfig(opts []ExtractOption) extractConfig {
        cfg := extractConfig{maxNewTokens: 512}
        for _, opt := range opts {
                if opt != nil {
                        opt(&cfg)
                }
        }
        if cfg.strict == nil {
                strict := true
                cfg.strict = &strict
        }
        return cfg
}

// extractionParameters normalizes the accepted schema forms into a
// parameters value: an ordered object for schemas this library builds, a
// plain map (serialized with the canonical key order) for caller-supplied
// ones, and the plain view of it for the validation passes.
func extractionParameters(schema any) (raw any, view map[string]any, err error) {
        switch v := schema.(type) {
        case nil:
                return nil, nil, fmt.Errorf("needle: extraction needs a schema")
        case *Tool:
                params := v.parameters
                if params == nil {
                        params = newObj().set("type", "object")
                }
                return params, paramsView(params), nil
        case string:
                var parsed any
                if err := json.Unmarshal([]byte(v), &parsed); err != nil {
                        return nil, nil, fmt.Errorf("needle: schema is not valid JSON: %w", err)
                }
                return extractionParameters(parsed)
        case map[string]any:
                params := schemaParametersOf(v)
                if params == nil {
                        params = map[string]any{}
                }
                return params, params, nil
        case *object:
                return v, v.view(), nil
        }
        return nil, nil, fmt.Errorf("needle: unsupported extraction schema of type %T", schema)
}

// paramsView returns the plain map view of a parameters value.
func paramsView(params any) map[string]any {
        if o, ok := params.(*object); ok {
                return o.view()
        }
        if m, ok := params.(map[string]any); ok {
                if v, ok := toPlain(m).(map[string]any); ok {
                        return v
                }
        }
        return map[string]any{}
}

func extractAs[A any](ctx context.Context, text string, rawParams any, view map[string]any, cfg extractConfig) (*A, error) {
        response, arguments, facts, err := runExtraction(ctx, text, rawParams, cfg)
        if err != nil {
                return nil, err
        }
        if arguments == nil {
                return nil, nil
        }
        if *cfg.strict {
                if err := validateExtraction(text, view, arguments, response, facts); err != nil {
                        return nil, err
                }
        }
        out, err := decodeArguments[A](arguments)
        if err != nil {
                return nil, fmt.Errorf("needle: %w", err)
        }
        return &out, nil
}

// runExtraction completes one turn against a single-tool schema on the
// shared one-shot agent, returning the response, the extracted arguments
// and the system facts the agent actually ran with (the caller's system
// plus the automatic date fact), which strict validation checks against.
// Extraction is not a separate mode: with one declared tool the grammar
// admits exactly one call of that name, so schema conformance is
// guaranteed rather than requested. A call the engine withheld into
// suppressed_calls still yields the record, exactly as the Python
// reference reads it back. The shared agent is stateless, so every
// extraction starts from a fresh conversation.
func runExtraction(ctx context.Context, text string, params any, cfg extractConfig) (*Response, map[string]any, string, error) {
        schema := newObj().
                set("name", "extract").
                set("description", "Extract structured data from the input.").
                set("parameters", params)
        toolsJSON, err := serializeTools([]any{schema})
        if err != nil {
                return nil, nil, "", err
        }
        opts := []Option{
                WithToolsJSON(toolsJSON),
                WithSystem(cfg.system),
        }
        if cfg.logger != nil {
                opts = append(opts, WithLogger(cfg.logger))
        }
        if cfg.weightsPath != "" {
                opts = append(opts, WithWeights(cfg.weightsPath))
        }
        if cfg.enginePath != "" {
                opts = append(opts, WithEnginePath(cfg.enginePath))
        }
        agent, err := sharedOneShot(opts...)
        if err != nil {
                return nil, nil, "", err
        }
        response, err := agent.Complete(ctx, text, MaxTokens(cfg.maxNewTokens))
        if err != nil {
                return nil, nil, "", err
        }
        facts := agent.system
        calls := response.FunctionCalls
        if len(calls) == 0 {
                calls = response.SuppressedCalls
        }
        if len(calls) == 0 {
                return response, nil, facts, nil
        }
        arguments := calls[0].Arguments
        if arguments == nil {
                arguments = map[string]any{}
        }
        return response, arguments, facts, nil
}
