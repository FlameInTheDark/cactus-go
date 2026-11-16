package cactus

import (
	"os"

	"github.com/FlameInTheDark/cactus-go/internal/engine"
)

// Worker-mode hook, part of the core: tuned model sessions (today the
// tuned Needle agents of the needle package) re-exec the host binary as a
// worker child (argv: --cactus-worker <nonce>; env: CACTUS_GO_WORKER=
// <nonce>). The init function below detects that combination before
// main() runs and takes the process over for the worker loop, so
// applications never need to cooperate. The needle package builds on this
// core, so any binary that can spawn a worker also carries the hook; the
// nonce makes accidental activation from a stray environment variable
// impossible: env value and argv value must match.
func init() {
	if engine.IsWorker() {
		os.Exit(engine.RunWorkerChild(os.Stdin))
	}
}

// IsWorker reports whether the current process was started as a worker
// child of a tuned model session. It is exported for applications that
// want to assert they are never accidentally running in worker mode.
func IsWorker() bool { return engine.IsWorker() }
