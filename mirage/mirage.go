// Package mirage is the vision model of the Cactus stack: image
// description, OCR, object detection with masks, and structured form
// filling, running on the same shared engine and install as Needle and
// Whistle.
//
//	vision, err := mirage.New()
//	result, err := vision.Describe(ctx, "photo.png",
//	        mirage.WithSections("objects", "text"), mirage.WithFields("bbox", "mask"))
//	form, err := vision.Extract(ctx, "photo.png",
//	        map[string]any{"animal": map[string]any{"enum": []any{"dog", "cat"}}})
//
// Images can be paths, encoded PNG/JPEG/GIF bytes, image.Image values, or
// packed RGB bytes with explicit dimensions (mirage.RGB). JPEGs are
// auto-rotated by their EXIF orientation, as Pillow does for the Python
// reference. Every call is serialized with Needle and Whistle through the
// shared engine.
package mirage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"sync"

	"github.com/FlameInTheDark/cactus-go/audio"
	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/internal/engine"
)

// defaultBufferSize is the initial JSON output buffer (16 MiB, matching
// the Python reference).
const defaultBufferSize = 16 * 1024 * 1024

// RGB is packed 8-bit image data: Data holds Width*Height*3 bytes in
// row-major order.
type RGB struct {
	Width  int
	Height int
	Data   []byte
}

// Mirage is a vision model instance bound to one weights archive (the
// shipped mirage.cact by default). The engine and weights are located -
// and downloaded on first use - lazily, with the context of the first
// call.
type Mirage struct {
	mu         sync.Mutex
	weights    string
	enginePath string
	bufSize    int
	logger     *slog.Logger

	lib *engine.Library
	buf []byte
}

// Option configures a Mirage model.
type Option func(*config)

type config struct {
	weights    string
	enginePath string
	bufSize    int
	logger     *slog.Logger
}

// WithWeights runs the model on a tuned .cact archive instead of the
// shipped mirage.cact.
func WithWeights(path string) Option {
	return func(c *config) { c.weights = path }
}

// WithEnginePath pins the native engine library to an explicit path.
func WithEnginePath(path string) Option {
	return func(c *config) { c.enginePath = path }
}

// WithBufferSize sets the JSON output buffer in bytes (default 16 MiB).
func WithBufferSize(size int) Option {
	return func(c *config) { c.bufSize = size }
}

// WithLogger installs a logger for download and engine lifecycle events.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) { c.logger = logger }
}

// New creates a Mirage model. Pass WithWeights to run a tuned archive.
func New(opts ...Option) *Mirage {
	cfg := config{bufSize: defaultBufferSize}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &Mirage{
		weights:    cfg.weights,
		enginePath: cfg.enginePath,
		bufSize:    cfg.bufSize,
		logger:     cfg.logger,
	}
}

// Weights reports the weights archive the model runs on.
func (m *Mirage) Weights() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.weights
}

// bindLocked resolves the engine and weights and verifies the archive is
// a vision model; m.mu must be held.
func (m *Mirage) bindLocked(ctx context.Context) error {
	libPath := m.enginePath
	if libPath == "" {
		var err error
		libPath, err = fetch.LibraryPath(ctx)
		if err != nil {
			return err
		}
	}
	lib, err := engine.LoadNative(libPath)
	if err != nil {
		return err
	}
	weights := m.weights
	if weights == "" {
		weights, err = fetch.WeightsPath(ctx, fetch.Mirage)
		if err != nil {
			return err
		}
		m.weights = weights
	}
	if !engine.WeightsLoaded(weights) {
		if err := lib.EnsureWeights(weights); err != nil {
			return err
		}
		if lib.Models()&engine.ModelVision == 0 {
			return fmt.Errorf("mirage: the selected archive is not a Mirage vision model")
		}
	}
	if m.buf == nil {
		size := m.bufSize
		if size <= 0 {
			size = defaultBufferSize
		}
		m.buf = make([]byte, size)
	}
	m.lib = lib
	return nil
}

// DescribeOption tunes one describe call. Settings are the keys the
// engine accepts: sections, fields, conf, text_conf, max_objects,
// max_text_lines, max_tokens, mask_format, nms and form.
type DescribeOption func(*describeConfig)

type describeConfig struct {
	order    []string       // option keys in application order
	settings map[string]any // explicit settings
	options  map[string]any // raw options dictionary
	language string
	keywords any
}

func (c *describeConfig) set(key string, value any) {
	if c.settings == nil {
		c.settings = map[string]any{}
	}
	if _, exists := c.settings[key]; !exists {
		c.order = append(c.order, key)
	}
	c.settings[key] = value
}

// WithSections selects which description sections the engine returns:
// scene, objects, regions, text, relations, events and reasoning. The
// engine rejects any other name.
func WithSections(sections ...string) DescribeOption {
	return func(c *describeConfig) { c.set("sections", sections) }
}

// WithFields selects per-object fields: bbox, score, point, position,
// description, count and mask. The engine rejects any other name; the
// detection threshold is the conf setting (WithConf), not a field.
func WithFields(fields ...string) DescribeOption {
	return func(c *describeConfig) { c.set("fields", fields) }
}

// WithConf sets the detection confidence threshold.
func WithConf(conf float64) DescribeOption {
	return func(c *describeConfig) { c.set("conf", conf) }
}

// WithTextConf sets the OCR confidence threshold.
func WithTextConf(conf float64) DescribeOption {
	return func(c *describeConfig) { c.set("text_conf", conf) }
}

// WithMaxObjects caps the number of detected objects.
func WithMaxObjects(n int) DescribeOption {
	return func(c *describeConfig) { c.set("max_objects", n) }
}

// WithMaxTextLines caps the number of OCR text lines.
func WithMaxTextLines(n int) DescribeOption {
	return func(c *describeConfig) { c.set("max_text_lines", n) }
}

// WithMaxTokens caps the response length in tokens.
func WithMaxTokens(n int) DescribeOption {
	return func(c *describeConfig) { c.set("max_tokens", n) }
}

// WithMaskFormat selects the mask encoding: largest, merged, polygons
// or bitmap.
func WithMaskFormat(format string) DescribeOption {
	return func(c *describeConfig) { c.set("mask_format", format) }
}

// WithNMS sets the non-maximum-suppression overlap threshold.
func WithNMS(nms float64) DescribeOption {
	return func(c *describeConfig) { c.set("nms", nms) }
}

// WithForm supplies a structured form to fill; see Extract.
func WithForm(form map[string]any) DescribeOption {
	return func(c *describeConfig) { c.set("form", form) }
}

// WithOptions merges a raw options dictionary into the call. A key both
// here and in an explicit setting is an error.
func WithOptions(options map[string]any) DescribeOption {
	return func(c *describeConfig) { c.options = options }
}

// WithLanguage sets the output language, as an ISO code. Empty follows
// the engine default.
func WithLanguage(language string) DescribeOption {
	return func(c *describeConfig) { c.language = language }
}

// WithKeywords biases recognition towards the given words and phrases.
func WithKeywords(keywords ...string) DescribeOption {
	return func(c *describeConfig) { c.keywords = keywords }
}

// mergedOptions builds the ordered options payload: the raw dictionary
// first (alphabetically, maps carry no order in Go), then the explicit
// settings in the order they were applied. Form field order is preserved
// because the payload is assembled by hand: encoding/json would sort map
// keys, and the engine reads ordered form fields.
func (c *describeConfig) mergedOptions() (string, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	writeKV := func(key string, value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		keyJSON, _ := json.Marshal(key)
		b.Write(keyJSON)
		b.WriteByte(':')
		b.Write(encoded)
		return nil
	}
	keys := make([]string, 0, len(c.options))
	for key := range c.options {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		if _, also := c.settings[key]; also {
			return "", fmt.Errorf("mirage: option %q was supplied both in options and as a setting", key)
		}
		if err := writeKV(key, c.options[key]); err != nil {
			return "", fmt.Errorf("mirage: describe options are not JSON-serializable: %w", err)
		}
	}
	for _, key := range c.order {
		if err := writeKV(key, c.settings[key]); err != nil {
			return "", fmt.Errorf("mirage: describe options are not JSON-serializable: %w", err)
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Describe runs the vision model over one image and returns its
// structured JSON result. Settings select sections, fields, thresholds
// and limits; language and keywords steer recognition like the speech
// APIs.
func (m *Mirage) Describe(ctx context.Context, input any, opts ...DescribeOption) (map[string]any, error) {
	cfg := describeConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	options, err := cfg.mergedOptions()
	if err != nil {
		return nil, err
	}
	if options == "" {
		options = "{}"
	}
	language, keywords, err := audio.Options(cfg.language, cfg.keywords)
	if err != nil {
		return nil, err
	}
	rgb, err := ToRGB(input)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.bindLocked(ctx); err != nil {
		return nil, err
	}
	raw, err := m.lib.Describe(rgb.Data, rgb.Width, rgb.Height, options, language, keywords, m.buf)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("mirage: engine returned an unparseable result (%v)", err)
	}
	return out, nil
}

// Extract fills an ordered form over one image. Each field is
// name -> {"enum": [...]} for a choice, {"type": "integer", "minimum":
// ..., "maximum": ...} for a bounded number, {"type": "string"} for free
// text, or {"x-source": "<field>"} to copy a box another field produced.
// The filled fields come back as a JSON object.
func (m *Mirage) Extract(ctx context.Context, input any, form map[string]any, opts ...DescribeOption) (map[string]any, error) {
	if len(form) == 0 {
		return nil, fmt.Errorf("mirage: form must be a nonempty map of fields")
	}
	return m.Describe(ctx, input, append(opts, WithForm(form))...)
}

// ToRGB converts an accepted image input into packed RGB bytes:
//
//   - *RGB / RGB: validated as-is
//   - string: a PNG/JPEG/GIF file path (JPEGs auto-rotate by EXIF)
//   - []byte: encoded PNG/JPEG/GIF bytes
//   - image.Image: any decoded image
func ToRGB(input any) (*RGB, error) {
	switch v := input.(type) {
	case nil:
		return nil, fmt.Errorf("mirage: describe needs an image")
	case *RGB:
		return validateRGB(v)
	case RGB:
		return validateRGB(&v)
	case string:
		raw, err := os.ReadFile(v)
		if err != nil {
			return nil, err
		}
		return decodeRGB(raw)
	case []byte:
		return decodeRGB(v)
	case image.Image:
		return packRGB(v), nil
	}
	return nil, fmt.Errorf("mirage: unsupported image input of type %T; pass a path, encoded bytes, an image.Image or mirage.RGB", input)
}

func validateRGB(rgb *RGB) (*RGB, error) {
	if rgb.Width <= 0 || rgb.Height <= 0 {
		return nil, fmt.Errorf("mirage: raw RGB input requires positive integer width and height")
	}
	if len(rgb.Data) != rgb.Width*rgb.Height*3 {
		return nil, fmt.Errorf("mirage: RGB data length must be width * height * 3")
	}
	return rgb, nil
}

// decodeRGB decodes PNG/JPEG/GIF bytes and applies EXIF orientation.
func decodeRGB(raw []byte) (*RGB, error) {
	// The stdlib decoders are registered by the blank imports above.
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mirage: cannot decode image: %w", err)
	}
	if orientation := jpegOrientation(raw); orientation > 1 {
		img = applyOrientation(img, orientation)
	}
	return packRGB(img), nil
}

// packRGB converts any decoded image into packed RGB bytes.
func packRGB(img image.Image) *RGB {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	data := make([]byte, width*height*3)
	at := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			data[at] = byte(r >> 8)
			data[at+1] = byte(g >> 8)
			data[at+2] = byte(b >> 8)
			at += 3
		}
	}
	return &RGB{Width: width, Height: height, Data: data}
}

// jpegOrientation reads the EXIF orientation (1-8) of a JPEG, or 1 for
// anything else. Only the segment walk and the IFD0 tag 0x0112 are
// parsed - the same subset Pillow's exif_transpose acts on.
func jpegOrientation(raw []byte) int {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return 1
	}
	at := 2
	for at+4 <= len(raw) {
		if raw[at] != 0xFF {
			return 1
		}
		marker := raw[at+1]
		switch {
		case marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01:
			at += 2 // standalone marker, no payload
			continue
		case marker == 0xDA || marker == 0xD9:
			return 1 // image data begins, no EXIF found
		}
		size := int(raw[at+2])<<8 | int(raw[at+3])
		if size < 2 || at+2+size > len(raw) {
			return 1
		}
		if marker == 0xE1 && at+10 <= len(raw) && string(raw[at+4:at+10]) == "Exif\x00\x00" {
			return exifOrientation(raw[at+10 : at+2+size])
		}
		at += 2 + size
	}
	return 1
}

// exifOrientation parses the TIFF header and IFD0 of an EXIF blob.
func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var little bool
	switch string(tiff[0:2]) {
	case "II":
		little = true
	case "MM":
		little = false
	default:
		return 1
	}
	u16 := func(b []byte) int {
		if little {
			return int(binary.LittleEndian.Uint16(b))
		}
		return int(binary.BigEndian.Uint16(b))
	}
	u32 := func(b []byte) int {
		if little {
			return int(binary.LittleEndian.Uint32(b))
		}
		return int(binary.BigEndian.Uint32(b))
	}
	if u16(tiff[2:4]) != 42 {
		return 1
	}
	ifd := u32(tiff[4:8])
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	entries := u16(tiff[ifd : ifd+2])
	for i := 0; i < entries; i++ {
		entry := ifd + 2 + i*12
		if entry+12 > len(tiff) {
			break
		}
		if u16(tiff[entry:entry+2]) == 0x0112 { // Orientation
			orientation := u16(tiff[entry+8 : entry+10])
			if orientation >= 1 && orientation <= 8 {
				return orientation
			}
			return 1
		}
	}
	return 1
}

// applyOrientation re-pixels an image according to its EXIF orientation,
// the way Pillow's exif_transpose does. Orientations 5-8 rotate by a
// quarter turn, so the output dimensions swap.
func applyOrientation(img image.Image, orientation int) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	quarter := orientation >= 5 && orientation <= 8
	outW, outH := width, height
	if quarter {
		outW, outH = height, width
	}
	out := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Source pixel in camera coordinates; target after undoing the
			// orientation.
			var tx, ty int
			switch orientation {
			case 2: // flip horizontal
				tx, ty = width-1-x, y
			case 3: // rotate 180
				tx, ty = width-1-x, height-1-y
			case 4: // flip vertical
				tx, ty = x, height-1-y
			case 5: // transpose
				tx, ty = y, x
			case 6: // rotate 90 CW
				tx, ty = height-1-y, x
			case 7: // transverse
				tx, ty = height-1-y, width-1-x
			case 8: // rotate 90 CCW
				tx, ty = y, width-1-x
			default:
				tx, ty = x, y
			}
			out.Set(tx, ty, img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return out
}

// The shared default model: package-level calls reuse one instance.
var (
	sharedMu  sync.Mutex
	sharedOne *Mirage
)

// Describe runs the vision model on the model this process already has
// loaded. See (*Mirage).Describe.
func Describe(ctx context.Context, input any, opts ...DescribeOption) (map[string]any, error) {
	sharedMu.Lock()
	if sharedOne == nil {
		sharedOne = New()
	}
	model := sharedOne
	sharedMu.Unlock()
	return model.Describe(ctx, input, opts...)
}

// Extract fills a form on the shared model. See (*Mirage).Extract.
func Extract(ctx context.Context, input any, form map[string]any, opts ...DescribeOption) (map[string]any, error) {
	sharedMu.Lock()
	if sharedOne == nil {
		sharedOne = New()
	}
	model := sharedOne
	sharedMu.Unlock()
	return model.Extract(ctx, input, form, opts...)
}
