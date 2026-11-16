package mirage

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlameInTheDark/cactus-go/internal/testutil"
)

// The tests run against the C stub engine from internal/testutil: the
// stub's describe returns the FNV-1a checksum of the exact options bytes
// plus the image geometry, so the ordered-options serialization and the
// image pipeline are both verified end to end.

// fnv1a replicates the stub's checksum over the options string.
func fnv1a(s string) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func stubEnv(t *testing.T, modelID byte) (lib, weights string) {
	t.Helper()
	lib = testutil.StubEngine(t)
	weights = testutil.FakeCact(t, t.TempDir(), modelID)
	return lib, weights
}

// stripedImage builds a 2x1 image: a red and a green pixel.
func stripedImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	return img
}

func TestDescribeEchoesOptionsAndGeometry(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))
	rgb := &RGB{Width: 2, Height: 1, Data: []byte{1, 2, 3, 4, 5, 6}}
	options := `{"sections":["objects"],"fields":["bbox"],"conf":0.5}`
	result, err := model.Describe(context.Background(), rgb,
		WithSections("objects"), WithFields("bbox"), WithConf(0.5),
		WithLanguage("en"), WithKeywords("k1", "k2"))
	if err != nil {
		t.Fatal(err)
	}
	if result["options_fnv"] != float64(fnv1a(options)) {
		t.Errorf("options_fnv = %v, want the hash of %q", result["options_fnv"], options)
	}
	if result["options_present"] != true {
		t.Error("options must reach the engine as a non-NULL string")
	}
	if result["width"] != float64(2) || result["height"] != float64(1) {
		t.Errorf("geometry = %vx%v", result["width"], result["height"])
	}
	if result["pixel_sum"] != float64(21) {
		t.Errorf("pixel_sum = %v, want 21", result["pixel_sum"])
	}
	if result["language"] != "en" || result["keywords"] != "k1\nk2" {
		t.Errorf("language/keywords = %v/%v", result["language"], result["keywords"])
	}
	if result["model"] != float64(31) {
		t.Errorf("model = %v, want the archive's id byte", result["model"])
	}
	if model.Weights() != weights {
		t.Errorf("Weights = %q, want %q", model.Weights(), weights)
	}
}

func TestDescribeEmptyOptions(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))
	result, err := model.Describe(context.Background(), &RGB{Width: 1, Height: 1, Data: []byte{9, 9, 9}})
	if err != nil {
		t.Fatal(err)
	}
	if result["options_fnv"] != float64(fnv1a("{}")) {
		t.Errorf("options_fnv = %v, want the hash of the empty object", result["options_fnv"])
	}
}

func TestOptionsOrderAndConflicts(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))
	rgb := &RGB{Width: 1, Height: 1, Data: []byte{0, 0, 0}}
	// The raw dictionary sorts first, then the explicit settings in
	// application order.
	result, err := model.Describe(context.Background(), rgb,
		WithOptions(map[string]any{"nms": 0.45}),
		WithMaxObjects(5), WithConf(0.25))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"nms":0.45,"max_objects":5,"conf":0.25}`
	if result["options_fnv"] != float64(fnv1a(want)) {
		t.Errorf("options_fnv = %v, want the hash of %q", result["options_fnv"], want)
	}
	// A key both in the dictionary and as a setting is an error.
	_, err = model.Describe(context.Background(), rgb,
		WithOptions(map[string]any{"conf": 0.9}), WithConf(0.25))
	if err == nil || !strings.Contains(err.Error(), "both in options and as a setting") {
		t.Fatalf("conflicting keys must be rejected, got %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestDescribeImageInputs(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))
	ctx := context.Background()

	// PNG bytes and a PNG path decode to the same packed RGB.
	pngBytes, err := encodePNG(stripedImage())
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := model.Describe(ctx, []byte(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stripe.png")
	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	fromPath, err := model.Describe(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	fromImage, err := model.Describe(ctx, stripedImage())
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []map[string]any{fromBytes, fromPath, fromImage} {
		if result["width"] != float64(2) || result["height"] != float64(1) {
			t.Fatalf("geometry = %vx%v", result["width"], result["height"])
		}
		// NRGBA is not premultiplied: 255,0,0 and 0,255,0 -> sum 510.
		if result["pixel_sum"] != float64(510) {
			t.Fatalf("pixel_sum = %v", result["pixel_sum"])
		}
	}

	// Unsupported inputs fail before reaching the engine.
	if _, err := model.Describe(ctx, 42); err == nil {
		t.Fatal("an int is not an image input")
	}
	if _, err := model.Describe(ctx, nil); err == nil {
		t.Fatal("nil must be rejected")
	}
	if _, err := model.Describe(ctx, []byte("not an image")); err == nil {
		t.Fatal("garbage bytes must fail to decode")
	}
	if _, err := ToRGB(&RGB{Width: 0, Height: 1}); err == nil {
		t.Fatal("zero dimensions must be rejected")
	}
	if _, err := ToRGB(&RGB{Width: 1, Height: 1, Data: []byte{1, 2}}); err == nil {
		t.Fatal("short data must be rejected")
	}
}

func TestDescribeEXIFRotation(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))

	// A 2x1 JPEG carrying an EXIF orientation of 6 (rotate 90 CW): the
	// engine must see the transposed 1x2 geometry, exactly as Pillow's
	// exif_transpose would hand it over.
	base, err := encodeJPEG(stripedImage())
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := spliceEXIF(base, 6)
	if err != nil {
		t.Fatal(err)
	}
	result, err := model.Describe(context.Background(), []byte(rotated))
	if err != nil {
		t.Fatal(err)
	}
	if result["width"] != float64(1) || result["height"] != float64(2) {
		t.Fatalf("geometry = %vx%v, want the EXIF-transposed 1x2", result["width"], result["height"])
	}
	// An orientation of 1 changes nothing; both decodes see the same
	// pixels (JPEG is lossy, so compare the two decodes, not the source).
	plain, err := spliceEXIF(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	plainResult, err := model.Describe(context.Background(), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if plainResult["width"] != float64(2) || plainResult["height"] != float64(1) {
		t.Fatalf("geometry = %vx%v, want the original 2x1", plainResult["width"], plainResult["height"])
	}
	if result["pixel_sum"] != plainResult["pixel_sum"] {
		t.Fatalf("pixel_sum = %v, want the unrotated %v (the pixels only move)",
			result["pixel_sum"], plainResult["pixel_sum"])
	}
}

func TestExtractFormReachesTheEngine(t *testing.T) {
	lib, weights := stubEnv(t, 31)
	model := New(WithEnginePath(lib), WithWeights(weights))
	rgb := &RGB{Width: 1, Height: 1, Data: []byte{0, 0, 0}}
	form := map[string]any{"animal": map[string]any{"enum": []any{"dog", "cat"}}}
	result, err := model.Extract(context.Background(), rgb, form)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"form":{"animal":{"enum":["dog","cat"]}}}`
	if result["options_fnv"] != float64(fnv1a(want)) {
		t.Errorf("options_fnv = %v, want the hash of %q", result["options_fnv"], want)
	}
	if _, err := model.Extract(context.Background(), rgb, nil); err == nil {
		t.Fatal("an empty form must be rejected")
	}
}

func TestPackageLevelDescribe(t *testing.T) {
	lib, _ := stubEnv(t, 31)
	weights := testutil.FakeCact(t, t.TempDir(), 31)
	t.Setenv("CACTUS_LIB_PATH", lib)
	t.Setenv("CACTUS_MIRAGE_WEIGHTS", weights)
	result, err := Describe(context.Background(), &RGB{Width: 1, Height: 1, Data: []byte{1, 1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result["pixel_sum"] != float64(3) {
		t.Fatalf("pixel_sum = %v", result["pixel_sum"])
	}
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf []byte
	if err := png.Encode(&buffer{&buf}, img); err != nil {
		return nil, err
	}
	return buf, nil
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf []byte
	if err := jpeg.Encode(&buffer{&buf}, img, nil); err != nil {
		return nil, err
	}
	return buf, nil
}

// buffer is a minimal io.Writer over a byte slice.
type buffer struct{ data *[]byte }

func (b *buffer) Write(p []byte) (int, error) {
	*b.data = append(*b.data, p...)
	return len(p), nil
}

// spliceEXIF inserts an APP1 Exif segment carrying the given orientation
// right after the JPEG SOI marker.
func spliceEXIF(jpegBytes []byte, orientation int) ([]byte, error) {
	if len(jpegBytes) < 4 || jpegBytes[0] != 0xFF || jpegBytes[1] != 0xD8 {
		return nil, fmt.Errorf("not a JPEG")
	}
	// TIFF: II, 42, IFD0 at 8; one Orientation entry; no next IFD.
	tiff := make([]byte, 26)
	binary.LittleEndian.PutUint16(tiff[0:2], 0x4949)
	binary.LittleEndian.PutUint16(tiff[2:4], 42)
	binary.LittleEndian.PutUint32(tiff[4:8], 8)
	binary.LittleEndian.PutUint16(tiff[8:10], 1) // one entry
	binary.LittleEndian.PutUint16(tiff[10:12], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:14], 3) // SHORT
	binary.LittleEndian.PutUint32(tiff[14:18], 1)
	binary.LittleEndian.PutUint16(tiff[18:20], uint16(orientation))
	binary.LittleEndian.PutUint32(tiff[22:26], 0)

	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := make([]byte, 0, 4+len(payload))
	segment = append(segment, 0xFF, 0xE1)
	segment = binary.BigEndian.AppendUint16(segment, uint16(len(payload)+2))
	segment = append(segment, payload...)
	out := make([]byte, 0, len(jpegBytes)+len(segment))
	out = append(out, jpegBytes[:2]...)
	out = append(out, segment...)
	out = append(out, jpegBytes[2:]...)
	return out, nil
}
