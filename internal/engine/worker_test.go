package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlameInTheDark/cactus-go/internal/testutil"
)

// TestMain gives this test binary the worker-mode hook the library's
// package init normally installs: a re-exec started with --cactus-worker
// runs the child loop instead of the tests.
func TestMain(m *testing.M) {
	if IsWorker() {
		os.Exit(RunWorkerChild(os.Stdin))
	}
	os.Exit(m.Run())
}

// stubEnv prepares a stub library and a fake tuned archive; it skips when
// no C compiler is available.
func stubEnv(t *testing.T) (libPath, weightsPath string) {
	t.Helper()
	libPath = testutil.StubEngine(t)
	weightsPath = testutil.FakeCact(t, t.TempDir(), 11)
	return libPath, weightsPath
}

func completeEnvelope(t *testing.T, text string) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("worker returned unparseable envelope %q: %v", text, err)
	}
	return envelope
}

func TestWorkerProtocolRoundTrip(t *testing.T) {
	lib, weights := stubEnv(t)
	worker, err := StartWorker(context.Background(), lib, weights, "", "[]", "", 4096, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if worker.PrefixTokens() != 7 {
		t.Fatalf("prefix tokens = %d, want 7 (cactus_init return)", worker.PrefixTokens())
	}

	envelope := completeEnvelope(t, mustComplete(t, worker, "first"))
	if envelope["model"] != float64(11) {
		t.Fatalf("model = %v, want 11 (the archive's id byte)", envelope["model"])
	}
	if got := envelope["threads"]; got != float64(0) {
		t.Fatalf("threads = %v, want 0", got)
	}
	if err := worker.Reset(); err != nil {
		t.Fatal(err)
	}
	envelope = completeEnvelope(t, mustComplete(t, worker, "again"))
	if envelope["resets"] != float64(1) {
		t.Fatalf("resets = %v, want 1 after Reset", envelope["resets"])
	}
}

func mustComplete(t *testing.T, worker *Worker, text string) string {
	t.Helper()
	out, err := worker.Complete(context.Background(), text, 8)
	if err != nil {
		t.Fatalf("complete %q: %v", text, err)
	}
	return out
}

func TestWorkersRunInIndependentProcesses(t *testing.T) {
	lib, _ := stubEnv(t)
	dir := t.TempDir()
	firstWeights := testutil.FakeCact(t, dir, 7)
	secondWeights := testutil.FakeCact(t, filepath.Join(dir, "second"), 13)
	first, err := StartWorker(context.Background(), lib, firstWeights, "", "[]", "", 4096, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := StartWorker(context.Background(), lib, secondWeights, "", "[]", "", 4096, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.cmd.Process.Pid == second.cmd.Process.Pid {
		t.Fatal("tuned agents must run in separate worker processes")
	}
	if model := completeEnvelope(t, mustComplete(t, first, "first"))["model"]; model != float64(7) {
		t.Fatalf("first worker model = %v, want 7", model)
	}
	if model := completeEnvelope(t, mustComplete(t, second, "second"))["model"]; model != float64(13) {
		t.Fatalf("second worker model = %v, want 13", model)
	}
	if err := first.Reset(); err != nil {
		t.Fatal(err)
	}
	if resets := completeEnvelope(t, mustComplete(t, first, "again"))["resets"]; resets != float64(1) {
		t.Fatalf("first resets = %v, want 1", resets)
	}
	if resets := completeEnvelope(t, mustComplete(t, second, "again"))["resets"]; resets != float64(0) {
		t.Fatalf("second resets = %v, want 0 (independent process)", resets)
	}
}

func TestWorkerUsesTheThreadCount(t *testing.T) {
	lib, weights := stubEnv(t)
	if err := SetThreads(3); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetThreads(0) })
	worker, err := StartWorker(context.Background(), lib, weights, "", "[]", "", 4096, 3, Threads())
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if threads := completeEnvelope(t, mustComplete(t, worker, "hi"))["threads"]; threads != float64(3) {
		t.Fatalf("threads = %v, want 3", threads)
	}
}

func TestSetThreadsRejectsNegatives(t *testing.T) {
	for _, bad := range []int{-1} {
		if err := SetThreads(bad); err == nil {
			t.Fatalf("SetThreads(%d) must fail", bad)
		}
	}
	if err := SetThreads(0); err != nil {
		t.Fatalf("SetThreads(0) must succeed: %v", err)
	}
}

func TestWorkerPropagatesNativeLoadFailure(t *testing.T) {
	lib, _ := stubEnv(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.cact")
	if err := os.WriteFile(bad, []byte{0x84, 0x2A, 0xE1, 0x05, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StartWorker(context.Background(), lib, bad, "", "[]", "", 4096, 3, 0)
	if err == nil {
		t.Fatal("a weights archive the engine rejects must fail the worker start")
	}
	if !strings.Contains(err.Error(), "stub engine error") && !strings.Contains(err.Error(), "cactus_load") {
		t.Fatalf("error should carry the engine detail, got %v", err)
	}
}

func TestWorkerEmbed(t *testing.T) {
	lib, weights := stubEnv(t)
	worker, err := StartWorker(context.Background(), lib, weights, "", "[]", "", 4096, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	embedding, err := worker.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{11, 12, 13}
	if len(embedding) != len(want) {
		t.Fatalf("embedding = %v, want %v", embedding, want)
	}
	for i := range want {
		if embedding[i] != want[i] {
			t.Fatalf("embedding = %v, want %v", embedding, want)
		}
	}
}

func TestWorkerCompleteAudio(t *testing.T) {
	lib, weights := stubEnv(t)
	dir := t.TempDir()
	speechWeights := testutil.FakeCact(t, dir, 21)
	worker, err := StartWorker(context.Background(), lib, weights, "", "[]", "", 1<<16, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	samples := make([]float32, 1600) // 0.1 s
	for i := range samples {
		samples[i] = float32(i%7) / 7
	}
	raw, err := worker.CompleteAudio(context.Background(), samples, AudioOptions{
		Language:      "en",
		Keywords:      "Siobhan\nKrzysztof",
		SpeechWeights: speechWeights,
	}, 16)
	if err != nil {
		t.Fatal(err)
	}
	envelope := completeEnvelope(t, raw)
	if envelope["audio_samples"] != float64(1600) {
		t.Fatalf("audio_samples = %v, want 1600", envelope["audio_samples"])
	}
	if envelope["language"] != "en" || envelope["keywords"] != "Siobhan\nKrzysztof" {
		t.Fatalf("audio options did not reach cactus_set_audio: %v", envelope)
	}
	if envelope["model"] != float64(21) {
		t.Fatalf("model = %v, want 21 (the speech weights loaded on demand)", envelope["model"])
	}
	if envelope["audio_text"] != "hello there" {
		t.Fatalf("audio_text = %v, want the transcript", envelope["audio_text"])
	}
}

func TestGenerationOfCact(t *testing.T) {
	if gen, err := GenerationOfCact([]byte{0x84, 0x2A, 0xE1, 0x05}); err != nil || gen != 3 {
		t.Fatalf("needle3 tag: gen=%d err=%v", gen, err)
	}
	if _, err := GenerationOfCact([]byte{0x83, 0x2A, 0xE1, 0x05}); err == nil {
		t.Fatal("needle2 archives must be refused with a clear error")
	}
	if _, err := GenerationOfCact([]byte{1, 2, 3}); err == nil {
		t.Fatal("short archives must be refused")
	}
	if _, err := GenerationOfCact([]byte{1, 2, 3, 4}); err == nil {
		t.Fatal("unknown tags must be refused")
	}
}

func TestReadCactRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "headed.cact")
	testutil.FakeHeadedCact(t, path, []byte{2})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tag, records, dataStart, err := ReadCactRecords(data)
	if err != nil {
		t.Fatal(err)
	}
	if tag != TagNeedle3 {
		t.Fatalf("tag = %#x, want needle3", tag)
	}
	// fp16(4), head(1), 6 fillers, raw: 9 records.
	if len(records) != 9 {
		t.Fatalf("records = %d, want 9", len(records))
	}
	if records[1].Dtype != DtypeFP16 || records[1].Dims[0] != 1 {
		t.Fatalf("head record = %+v", records[1])
	}
	if records[8].Dtype != DtypeRaw {
		t.Fatalf("last record dtype = %d, want raw", records[8].Dtype)
	}
	if int(records[8].Offset)+int(records[8].Length) > len(data) || dataStart <= 0 {
		t.Fatal("record offsets must address bytes inside the archive")
	}
}
