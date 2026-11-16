package cactus

// Version is the version of the cactus-go library.
//
// Release builds override it at link time with the release tag, so a shipped
// binary reports the version it was released under:
//
//	go build -ldflags "-X github.com/FlameInTheDark/cactus-go.Version=v1.2.3" ./cmd/cactus
//
// Builds made straight from source report the fallback value below.
var Version = "0.5.0"

// EngineGeneration is the engine generation this package targets. The
// base model shipped with generation 3 weights is the Needle 3
// tool-calling model (29-121M parameters, 2-20 layers, 8-29 MB).
const EngineGeneration = 3

// EngineVersion reports the pinned Cactus engine build the library
// downloads.
const EngineVersion = "4.1.1"
