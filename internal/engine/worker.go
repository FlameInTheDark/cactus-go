package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unsafe"
)

// Worker runs a tuned engine in a child process and speaks the
// length-prefixed JSON protocol over its stdin/stdout. The child is a
// re-exec of the current binary started with the "--cactus-worker"
// argument and a matching CACTUS_GO_WORKER nonce; the library's package
// init detects this and runs the child loop before main() gets a chance
// to execute. Each tuned agent gets its own process, because the engine
// keeps one global text session.
type Worker struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	mu      sync.Mutex
	closed  bool
	prefix  int
	exited  chan struct{}
	waitErr error
	once    sync.Once
}

// WorkerArg is the argv marker that selects worker mode.
const WorkerArg = "--cactus-worker"

// WorkerEnv is the environment variable carrying the worker nonce.
const WorkerEnv = "CACTUS_GO_WORKER"

// IsWorker reports whether the current process was started as a worker
// child by StartWorker.
func IsWorker() bool {
	return len(os.Args) >= 3 && os.Args[1] == WorkerArg &&
		os.Getenv(WorkerEnv) == os.Args[2]
}

// WorkerNonce returns the nonce a parent passes when re-execing itself.
func WorkerNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("w%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// StartWorker launches the child process and initializes it with the
// given configuration. The child applies the thread count, loads the
// weights archive and binds the toolset before reporting readiness.
func StartWorker(ctx context.Context, libPath, weightsPath, system, toolsJSON, toolIndex string, bufSize, generation, threads int) (*Worker, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cactus: cannot re-exec worker (os.Executable: %w)", err)
	}
	nonce := WorkerNonce()
	cmd := exec.Command(exe, WorkerArg, nonce)
	cmd.Env = append(os.Environ(), WorkerEnv+"="+nonce)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cactus: cannot start worker process: %w", err)
	}
	w := &Worker{cmd: cmd, stdin: stdin, stdout: stdout, exited: make(chan struct{})}
	go func() { w.waitErr = cmd.Wait(); close(w.exited) }()

	if bufSize <= 0 {
		bufSize = 65536
	}
	cfg := workerConfig{
		Library:    libPath,
		Threads:    threads,
		Weights:    weightsPath,
		System:     system,
		Tools:      toolsJSON,
		ToolIndex:  toolIndex,
		BufferSize: bufSize,
		Generation: generation,
	}
	if err := writeMessage(stdin, cfg); err != nil {
		w.Close()
		return nil, fmt.Errorf("cactus: worker failed to start: %w", err)
	}
	// Allow a generous startup budget: the child loads the .cact archive
	// and initializes the engine before reporting readiness.
	startup := 300 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < startup {
			startup = remaining
		}
	}
	var ready workerResponse
	if err := w.receive(ctx, &ready, startup); err != nil {
		w.Close()
		return nil, fmt.Errorf("cactus: worker did not initialize: %w", err)
	}
	if ready.Status != "ready" {
		w.Close()
		return nil, fmt.Errorf("cactus: worker failed to start: %s", ready.Message)
	}
	w.prefix = ready.PrefixTokens
	return w, nil
}

// PrefixTokens reports the prompt prefix token count the child observed
// after loading weights and initializing the engine.
func (w *Worker) PrefixTokens() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prefix
}

// receive reads one response message, honoring ctx and timeout.
func (w *Worker) receive(ctx context.Context, out *workerResponse, timeout time.Duration) error {
	type result struct {
		msg workerResponse
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var msg workerResponse
		err := readMessage(w.stdout, &msg)
		ch <- result{msg, err}
	}()
	var timerC <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timerC = t.C
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timerC:
		return fmt.Errorf("worker did not respond in time")
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("worker exited unexpectedly: %w", r.err)
		}
		*out = r.msg
		return nil
	}
}

// request sends one operation and waits for its reply.
func (w *Worker) request(ctx context.Context, req workerRequest) (*workerResponse, error) {
	if err := writeMessage(w.stdin, req); err != nil {
		return nil, fmt.Errorf("cactus: worker exited unexpectedly: %w", err)
	}
	var resp workerResponse
	if err := w.receive(ctx, &resp, 0); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Complete runs one text inference turn in the child.
func (w *Worker) Complete(ctx context.Context, input string, maxNewTokens int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return "", fmt.Errorf("cactus: worker is closed")
	}
	resp, err := w.request(ctx, workerRequest{
		Operation:    "complete",
		Text:         input,
		MaxNewTokens: maxNewTokens,
	})
	if err != nil {
		return "", err
	}
	if resp.Status != "ok" {
		return "", fmt.Errorf("cactus: %s", resp.Message)
	}
	return resp.Response, nil
}

// CompleteAudio runs one speech-driven turn in the child. The child loads
// the speech weights named by opts on demand.
func (w *Worker) CompleteAudio(ctx context.Context, samples []float32, opts AudioOptions, maxNewTokens int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return "", fmt.Errorf("cactus: worker is closed")
	}
	resp, err := w.request(ctx, workerRequest{
		Operation:      "complete_audio",
		Audio:          encodeSamples(samples),
		SpeechWeights:  opts.SpeechWeights,
		Language:       opts.Language,
		Keywords:       opts.Keywords,
		WordTimestamps: opts.WordTimestamps,
		MaxNewTokens:   maxNewTokens,
	})
	if err != nil {
		return "", err
	}
	if resp.Status != "ok" {
		return "", fmt.Errorf("cactus: %s", resp.Message)
	}
	return resp.Response, nil
}

// Embed returns the text embedding of the input, computed in the child.
func (w *Worker) Embed(ctx context.Context, text string) ([]float32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, fmt.Errorf("cactus: worker is closed")
	}
	resp, err := w.request(ctx, workerRequest{Operation: "embed", Text: text})
	if err != nil {
		return nil, err
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("cactus: %s", resp.Message)
	}
	return resp.Embedding, nil
}

// Reset rewinds the conversation in the child.
func (w *Worker) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("cactus: worker is closed")
	}
	resp, err := w.request(context.Background(), workerRequest{Operation: "reset"})
	if err != nil {
		return err
	}
	if resp.Status != "ok" {
		return fmt.Errorf("cactus: %s", resp.Message)
	}
	return nil
}

// Close shuts the worker down: best-effort handshake, then a graceful
// timeout, then escalation to kill.
func (w *Worker) Close() error {
	w.once.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed {
			return
		}
		w.closed = true
		if w.cmd.Process != nil {
			_ = writeMessage(w.stdin, workerRequest{Operation: "close"})
			_ = w.stdin.Close()
			select {
			case <-w.exited:
			case <-time.After(2 * time.Second):
				_ = w.cmd.Process.Kill()
				<-w.exited
			}
		}
		_ = w.stdout.Close()
	})
	return nil
}

// encodeSamples packs float32 samples as base64 little-endian bytes, the
// worker wire form of audio.
func encodeSamples(samples []float32) string {
	if len(samples) == 0 {
		return ""
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*4)
	return base64.StdEncoding.EncodeToString(raw)
}

// decodeSamples unpacks the worker wire form of audio back into samples.
func decodeSamples(encoded string) ([]float32, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid PCM buffer: %w", err)
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("invalid PCM buffer")
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out, nil
}

func float32frombits(v uint32) float32 { return *(*float32)(unsafe.Pointer(&v)) }

// RunWorkerChild implements the child side of the protocol on the given
// streams. It returns the process exit code. The function never returns
// normally in interactive use; callers should os.Exit with the result.
func RunWorkerChild(proto io.Reader) int {
	// Route C-level stdout writes of the engine away from the protocol
	// stream: on Unix fd 1 is re-pointed at stderr while the protocol
	// keeps a duplicate of the parent pipe.
	stdout := redirectChildStdout()

	var cfg workerConfig
	if err := readMessage(proto, &cfg); err != nil {
		return 1
	}
	lib, err := LoadNative(cfg.Library)
	if err != nil {
		_ = writeMessage(stdout, workerResponse{Status: "fatal", Message: err.Error()})
		return 1
	}
	rtMu.Lock()
	lib.lib.setThreads(int32(cfg.Threads))
	speechWeights := ""
	rtMu.Unlock()

	if err := lib.EnsureWeights(cfg.Weights); err != nil {
		_ = writeMessage(stdout, workerResponse{Status: "fatal", Message: err.Error()})
		return 1
	}
	prefix := lib.lib.init(cstring(cfg.System), cstring(cfg.Tools), cstring(cfg.ToolIndex))
	if prefix < 0 {
		detail := lib.LastError()
		if detail == "" {
			detail = fmt.Sprintf("cactus_init failed (code %d)", prefix)
		}
		_ = writeMessage(stdout, workerResponse{Status: "fatal", Message: detail})
		return 1
	}
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = 65536
	}
	buf := make([]byte, bufSize)
	if err := writeMessage(stdout, workerResponse{Status: "ready", PrefixTokens: int(prefix)}); err != nil {
		return 1
	}
	for {
		var req workerRequest
		if err := readMessage(proto, &req); err != nil {
			if err == io.EOF {
				return 0
			}
			_ = writeMessage(stdout, workerResponse{Status: "fatal", Message: err.Error()})
			return 1
		}
		switch req.Operation {
		case "complete", "complete_audio":
			var (
				pcm     *float32
				count   int
				textPtr *byte
			)
			if req.Operation == "complete_audio" {
				if req.SpeechWeights != "" && req.SpeechWeights != speechWeights {
					if err := lib.EnsureWeights(req.SpeechWeights); err != nil {
						_ = writeMessage(stdout, workerResponse{Status: "error", Message: err.Error()})
						continue
					}
					speechWeights = req.SpeechWeights
				}
				samples, err := decodeSamples(req.Audio)
				if err != nil {
					_ = writeMessage(stdout, workerResponse{Status: "error", Message: err.Error()})
					continue
				}
				pcm, count = float32Ptr(samples)
				timestamps := int32(0)
				if req.WordTimestamps {
					timestamps = 1
				}
				lib.lib.setAudio(cstring(req.Language), cstring(req.Keywords), timestamps)
			} else {
				textPtr = cstring(req.Text)
			}
			maxTok := req.MaxNewTokens
			if maxTok <= 0 {
				maxTok = 512
			}
			rc := lib.lib.complete(textPtr, pcm, int32(count), int32(maxTok), &buf[0], int32(len(buf)))
			if rc < 0 {
				detail := cbufferString(buf)
				if detail == "" {
					detail = lib.LastError()
				}
				if detail == "" {
					detail = fmt.Sprintf("cactus_complete failed (code %d)", rc)
				}
				_ = writeMessage(stdout, workerResponse{Status: "error", Message: detail})
			} else {
				_ = writeMessage(stdout, workerResponse{Status: "ok", Response: cbufferString(buf)})
			}
		case "embed":
			input := cstring(req.Text)
			dim := lib.lib.embed(input, nil, 0, nil, 0)
			if dim <= 0 {
				_ = writeMessage(stdout, workerResponse{Status: "error",
					Message: fmt.Sprintf("cactus_embed failed (code %d)", dim)})
				continue
			}
			embedding := make([]float32, dim)
			if rc := lib.lib.embed(input, nil, 0, &embedding[0], dim); rc != dim {
				_ = writeMessage(stdout, workerResponse{Status: "error",
					Message: fmt.Sprintf("cactus_embed failed (code %d)", rc)})
				continue
			}
			_ = writeMessage(stdout, workerResponse{Status: "ok", Embedding: embedding})
		case "reset":
			lib.lib.reset()
			_ = writeMessage(stdout, workerResponse{Status: "ok"})
		case "close":
			_ = writeMessage(stdout, workerResponse{Status: "ok"})
			return 0
		default:
			_ = writeMessage(stdout, workerResponse{Status: "error",
				Message: fmt.Sprintf("unknown worker operation: %s", req.Operation)})
		}
	}
}
