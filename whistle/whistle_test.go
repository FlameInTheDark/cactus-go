package whistle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/FlameInTheDark/cactus-go/audio"
	"github.com/FlameInTheDark/cactus-go/internal/testutil"
)

// The tests run against the C stub engine from internal/testutil: the
// stub's transcribe and stream primitives echo their arguments, so every
// assertion below is deterministic and offline.

// stubEnv binds a Whistle to the stub engine with a fake tuned archive.
func stubEnv(t *testing.T, modelID byte) (lib, weights string) {
	t.Helper()
	lib = testutil.StubEngine(t)
	weights = testutil.FakeCact(t, t.TempDir(), modelID)
	return lib, weights
}

func mono(samples int) []float32 {
	out := make([]float32, samples)
	for i := range out {
		out[i] = float32(i%7) / 7
	}
	return out
}

func TestTranscribeEchoesOptions(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	model := New(WithEnginePath(lib), WithWeights(weights))
	result, err := model.Transcribe(context.Background(), mono(1600),
		WithLanguage("en"), WithKeywords("Siobhan", "Krzysztof"), WordTimestamps(true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "samples 1600" {
		t.Errorf("text = %q, want the echoed sample count", result.Text)
	}
	if result.Language != "en" {
		t.Errorf("language = %q", result.Language)
	}
	if result.TTFTMS != 12.5 || result.DecodeTPS != 900.5 {
		t.Errorf("timings = %v/%v", result.TTFTMS, result.DecodeTPS)
	}
	if len(result.Words) != 1 || result.Words[0].Word != "hello" ||
		result.Words[0].Start != 0.1 || result.Words[0].End != 0.4 ||
		result.Words[0].Probability != 0.98 {
		t.Errorf("words = %+v", result.Words)
	}
	// The weights accessor reports the archive after the first call.
	if model.Weights() != weights {
		t.Errorf("Weights = %q, want %q", model.Weights(), weights)
	}
}

func TestTranscribeWAVPath(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	// 160 samples of 0.5 amplitude, 16 kHz mono s16.
	payload := make([]byte, 0, 44+160*2)
	payload = append(payload,
		'R', 'I', 'F', 'F', 100, 1, 0, 0, 'W', 'A', 'V', 'E', // 36 + 320 bytes
		'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0,
		0x80, 0x3E, 0, 0, // 16000
		0x00, 0x7D, 0, 0, // byte rate
		2, 0, 16, 0,
		'd', 'a', 't', 'a', 0x40, 0x01, 0, 0) // 320 data bytes
	for i := 0; i < 160; i++ {
		v := int16(16384 * (i % 2)) // alternating 0.5 and 0
		payload = append(payload, byte(v), byte(v>>8))
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	model := New(WithEnginePath(lib), WithWeights(weights))
	result, err := model.Transcribe(context.Background(), path, WithLanguage("de"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "samples 160" {
		t.Errorf("text = %q, want 160 samples from the WAV", result.Text)
	}
}

func TestTranscribeRejectsBadInputs(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	model := New(WithEnginePath(lib), WithWeights(weights))
	if _, err := model.Transcribe(context.Background(), 42); err == nil {
		t.Fatal("an int is not an audio input")
	}
	if _, err := model.Transcribe(context.Background(), "missing.wav"); err == nil {
		t.Fatal("a missing WAV path must fail")
	}
}

func TestStreamCommitsAndStops(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	model := New(WithEnginePath(lib), WithWeights(weights))
	ctx := context.Background()

	chunks := [][]float32{mono(1600), mono(3200)}
	var texts []string
	var received []float64
	stepIndex := 0
	for step, err := range model.Stream(ctx, slices.Values(chunks), WithLanguage("en")) {
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, step.Text)
		received = append(received, step.Received)
		if stepIndex < 2 { // the committed chunk steps carry the tail
			if step.Pending != "tail" {
				t.Errorf("pending = %q, want the unconfirmed tail", step.Pending)
			}
		} else if step.Pending != "" { // the stop step commits everything
			t.Errorf("pending = %q, want empty after the stop", step.Pending)
		}
		stepIndex++
	}
	if fmt.Sprint(texts) != fmt.Sprint([]string{"chunk 1600", "chunk 3200", "tail"}) {
		t.Errorf("steps = %v", texts)
	}
	if received[0] != 0.1 || received[1] != 0.2 || received[2] != 1.0 {
		t.Errorf("received seconds = %v", received)
	}
}

func TestStreamEarlyBreakStopsCleanly(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	model := New(WithEnginePath(lib), WithWeights(weights))
	ctx := context.Background()

	seen := 0
	for range model.Stream(ctx, func(yield func([]float32) bool) {
		if !yield(mono(1600)) {
			return
		}
		if !yield(mono(3200)) {
			return
		}
	}) {
		seen++
		break // consumer stops early; the stream must stop cleanly
	}
	if seen != 1 {
		t.Fatalf("steps = %d", seen)
	}
	// The engine lock was released: a full stream works right after.
	steps := 0
	for _, err := range model.Stream(ctx, func(yield func([]float32) bool) {
		yield(mono(1600))
	}, WithLanguage("en")) {
		if err != nil {
			t.Fatal(err)
		}
		steps++
	}
	if steps != 2 { // chunk + stop tail
		t.Fatalf("second stream steps = %d", steps)
	}
}

func TestEmbedAudioWindows(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	model := New(WithEnginePath(lib), WithWeights(weights))
	// 1600 samples = 2 frames of 1280 -> 2*512 floats, values (i%97)*0.5.
	embedding, err := model.Embed(context.Background(), mono(1600))
	if err != nil {
		t.Fatal(err)
	}
	if len(embedding) != 1024 {
		t.Fatalf("len = %d, want 1024", len(embedding))
	}
	if embedding[0] != 0 || embedding[1] != 0.5 || embedding[97] != 0 || embedding[96] != 48 {
		t.Fatalf("embedding head = %v", embedding[:3])
	}
}

func TestOutputBufferGrowsWithAudio(t *testing.T) {
	lib, weights := stubEnv(t, 21)
	// A 30 s clip needs a 122880-byte JSON buffer; the model must grow
	// the initial 64-byte buffer before calling the engine.
	model := New(WithEnginePath(lib), WithWeights(weights), WithBufferSize(64))
	result, err := model.Transcribe(context.Background(), mono(30*audio.SampleRate))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != fmt.Sprintf("samples %d", 30*audio.SampleRate) {
		t.Fatalf("text = %q, want the full untruncated envelope", result.Text)
	}
}

func TestLanguagesOfReadsTheTokenizer(t *testing.T) {
	dir := t.TempDir()
	advertised := filepath.Join(dir, "advertised.cact")
	testutil.FakeTokenizerCact(t, advertised, []string{"en", "de", "fr"})
	if got := LanguagesOf(advertised); fmt.Sprint(got) != fmt.Sprint([]string{"en", "de", "fr"}) {
		t.Errorf("LanguagesOf = %v, want the advertised set", got)
	}
	// Broken or foreign archives fall back to the shipped list.
	headless := testutil.FakeCact(t, filepath.Join(dir, "headless"), 21)
	if got := LanguagesOf(headless); !slices.Equal(got, Languages) {
		t.Errorf("LanguagesOf(headless) = %v, want %v", got, Languages)
	}
	if got := LanguagesOf(filepath.Join(dir, "missing.cact")); !slices.Equal(got, Languages) {
		t.Errorf("LanguagesOf(missing) = %v, want %v", got, Languages)
	}
}

// The package-level API binds through the environment overrides, so it
// reaches the stub without a network round trip.
func TestPackageLevelTranscribe(t *testing.T) {
	lib, _ := stubEnv(t, 21)
	weights := testutil.FakeCact(t, t.TempDir(), 21)
	t.Setenv("CACTUS_LIB_PATH", lib)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", weights)
	result, err := Transcribe(context.Background(), mono(800))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "samples 800" {
		t.Fatalf("text = %q", result.Text)
	}
	// Explicit weights use their own shared instance.
	other := testutil.FakeCact(t, filepath.Join(t.TempDir(), "other"), 21)
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", other)
	result, err = TranscribeWith(context.Background(), other, mono(400))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "samples 400" {
		t.Fatalf("text = %q", result.Text)
	}
}
