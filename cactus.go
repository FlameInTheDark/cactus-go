// Package cactus is the core of the Cactus stack: the shared on-device
// inference engine, its install and its version pins. One native library
// (libcactus.so / .dll / .dylib) runs every Cactus model, and the three
// models are separate .cact weight archives loaded into it:
//
//   - Needle 3, text: tool calling, structured extraction, embeddings -
//     github.com/FlameInTheDark/cactus-go/needle
//   - Whistle, speech: transcription, timestamps, live streaming -
//     github.com/FlameInTheDark/cactus-go/whistle
//   - Mirage, vision: description, OCR, masks, form filling -
//     github.com/FlameInTheDark/cactus-go/mirage
//
// This package is what the models share: engine-wide settings, the loaded
// model registry and the worker-process machinery. Picking a model means
// picking its package, never this one:
//
//	agent, err := needle.New(needle.WithTools(sendEmail))
//	text, err := whistle.Transcribe(ctx, "memo.wav")
//	form, err := mirage.Describe(ctx, "photo.png")
//
// The engine, the base weights and the tuned archives are fetched once
// from the Hugging Face Hub on first use and cached under ~/.cache/cactus.
// Inference itself never touches the network. Pure Go toolchain, no cgo;
// the single low-level dependency (purego) wraps dlopen/LoadLibrary.
package cactus

import (
	"context"

	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/internal/engine"
)

// SetThreads sets the CPU thread count for every model loaded after the
// call, tuned worker children included, or 0 (the default) to use the
// cores the process may run on. It applies to Needle, Whistle and Mirage
// alike: they share one engine.
func SetThreads(threads int) error { return engine.SetThreads(threads) }

// Models returns the bitmask of model types currently loaded into the
// engine: ModelText (1), ModelSpeech (2) and ModelVision (4). It locates
// the engine library the way the model packages do.
func Models(ctx context.Context) (int32, error) {
	libPath, err := fetch.LibraryPath(ctx)
	if err != nil {
		return 0, err
	}
	lib, err := engine.LoadNative(libPath)
	if err != nil {
		return 0, err
	}
	return lib.Models(), nil
}

// Model bits reported by Models, matching the cactus_models() ABI: a
// loaded archive advertises the model kinds it carries.
const (
	ModelText   = 1
	ModelSpeech = 2
	ModelVision = 4
)
