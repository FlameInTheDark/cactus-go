package needle

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// This file gives the Needle text model the same zero-setup package-level
// calls Whistle and Mirage have: whistle.Transcribe and mirage.Describe
// need no construction, and neither do Run, Complete, CompleteAudio and
// Embed here. One shared agent serves every call.
//
//      resp, err := needle.Run(ctx, "email finance@example.com about expenses",
//              needle.RunTools(sendEmail))
//
// The engine keeps a single text session per process, so one shared
// one-shot agent is cached, keyed by everything that configures the
// session (toolset, system facts, weights, engine path, buffer, logger):
// a call with the same identity reuses it, a call with a new identity
// closes the previous one and builds a fresh agent - the same sharing the
// Python reference's extract() applies. The shared agent is stateless, so
// every one-shot call starts from a fresh conversation. Alternating
// identities re-initializes on each switch; for repeated multi-turn work
// build a Needle instead.

var (
	sharedMu    sync.Mutex
	sharedAgent *Needle
	sharedKey   string
)

// oneShotKey is the identity of an agent: every field that changes how
// its engine session is configured. Two agents with equal keys answer
// one-shot calls identically, so the cheaper one to keep is the cached
// one.
func (a *Needle) oneShotKey() string {
	return fmt.Sprintf("tools=%s\x1fsystem=%s\x1fweights=%s\x1fengine=%s\x1findex=%s\x1fbuf=%d\x1fstateless=%v\x1flogger=%p",
		a.toolsJSON, a.system, a.weightsPath, a.enginePath, a.toolIndexPath,
		a.bufSize, a.stateless, a.logger)
}

// sharedOneShot returns the shared one-shot agent for the given
// construction options, building it on first use or when the identity
// changed and closing the previous agent otherwise. The returned agent is
// stateless: each call resets the conversation before running.
func sharedOneShot(opts ...Option) (*Needle, error) {
	candidate, err := New(append(opts, WithStateless(true))...)
	if err != nil {
		return nil, err
	}
	key := candidate.oneShotKey()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedAgent != nil && sharedKey == key {
		// Same identity: keep the bound session, discard the candidate.
		_ = candidate.Close()
		return sharedAgent, nil
	}
	if sharedAgent != nil {
		_ = sharedAgent.Close()
	}
	sharedAgent = candidate
	sharedKey = key
	return candidate, nil
}

// oneShotOptions converts one-shot identity fields into agent
// construction options.
func oneShotOptions(src any, system, weights, enginePath string, logger *slog.Logger) []Option {
	opts := []Option{}
	if src != nil {
		opts = append(opts, Option(func(c *agentConfig) { c.toolsSource = src }))
	}
	if system != "" {
		opts = append(opts, WithSystem(system))
	}
	if weights != "" {
		opts = append(opts, WithWeights(weights))
	}
	if enginePath != "" {
		opts = append(opts, WithEnginePath(enginePath))
	}
	if logger != nil {
		opts = append(opts, WithLogger(logger))
	}
	return opts
}

func (c completeConfig) oneShotOptions() []Option {
	return oneShotOptions(c.toolsSource, c.system, c.weightsPath, c.enginePath, c.logger)
}

func (c runConfig) oneShotOptions() []Option {
	return oneShotOptions(c.toolsSource, c.system, c.weightsPath, c.enginePath, c.logger)
}

// Run drives one agentic query on the Needle 3 model this process already
// has loaded: the model picks calls, Run executes the declared handlers,
// feeds the results back, and returns the final response with every
// executed result in Results. It is the zero-setup counterpart of
// (*Needle).Run - whistle.Transcribe and mirage.Describe are its peers -
// and every call starts from a fresh conversation.
//
//	resp, err := needle.Run(ctx, "email finance@example.com about expenses",
//	        needle.RunTools(sendEmail), needle.RunSystem("date: 2026-11-16"))
//
// Loop tuning (MaxSteps, RunMaxTokens, RunStrict) and identity (RunTools,
// RunToolsJSON, RunSystem, RunWeights, RunEnginePath, RunLogger) are
// RunOptions. Calls against tools declared without handlers answer with
// an "unknown tool" error result, exactly as on an agent. Repeated calls
// with the same identity share one engine session; for multi-turn
// conversations build a Needle with New.
func Run(ctx context.Context, query string, opts ...RunOption) (*Response, error) {
	cfg := runConfig{maxSteps: 8, maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	agent, err := sharedOneShot(cfg.oneShotOptions()...)
	if err != nil {
		return nil, err
	}
	return agent.run(ctx, query, cfg)
}

// Complete performs one turn on the Needle 3 model this process already
// has loaded and returns the response envelope without executing any
// calls: execute them (or not) and feed results back with the next
// Complete on an agent. It is the zero-setup counterpart of
// (*Needle).Complete; every call starts from a fresh conversation.
//
//	resp, err := needle.Complete(ctx, "CALL: ...", needle.CompleteTools(sendEmail))
//
// Identity options (CompleteTools, CompleteToolsJSON, CompleteSystem,
// CompleteWeights, CompleteEnginePath, CompleteLogger) build the shared
// agent; MaxTokens caps the response length.
func Complete(ctx context.Context, text string, opts ...CompleteOption) (*Response, error) {
	cfg := completeConfig{maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	agent, err := sharedOneShot(cfg.oneShotOptions()...)
	if err != nil {
		return nil, err
	}
	return agent.complete(ctx, text, cfg)
}

// CompleteAudio performs one speech-driven turn on the Needle 3 model
// this process already has loaded: the clip is transcribed and fed
// through the tool-calling grammar in a single pass, and the response
// envelope carries the transcript in AudioText. It is the zero-setup
// counterpart of (*Needle).CompleteAudio; every call starts from a fresh
// conversation. input is a WAV path, raw float32 LE bytes or a sample
// slice.
//
//	resp, err := needle.CompleteAudio(ctx, "memo.wav",
//	        needle.CompleteTools(sendEmail), needle.WithLanguage("en"))
func CompleteAudio(ctx context.Context, input any, opts ...CompleteOption) (*Response, error) {
	cfg := completeConfig{maxNewTokens: 512}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	agent, err := sharedOneShot(cfg.oneShotOptions()...)
	if err != nil {
		return nil, err
	}
	return agent.completeAudio(ctx, input, cfg)
}

// Embed returns the text embedding of the input from the Needle 3 model
// this process already has loaded, the peer of whistle.Embed. It accepts
// the same construction options as New - weights, engine path, logger -
// none of which a plain call needs:
//
//	vector, err := needle.Embed(ctx, "a red umbrella")
func Embed(ctx context.Context, text string, opts ...Option) ([]float32, error) {
	agent, err := sharedOneShot(opts...)
	if err != nil {
		return nil, err
	}
	// Embeddings do not touch the conversation, so no reset is needed.
	return agent.Embed(ctx, text)
}
