# Vision (Mirage)

Mirage is the vision model of the Cactus stack: image description, OCR,
object detection with masks and structured form filling, running on the
same shared engine and the same install as Needle and Whistle.

Like the other two models it exposes two shapes: **one-shot package
calls** (`mirage.Describe`, `mirage.Extract`) bound to a shared default
model, and a **constructed model** (`mirage.New`) for repeated work or
tuned weights. Every result is structured JSON (`map[string]any`) — there
is no free-text description mode.

- [Describing an image](#describing-an-image)
- [Sections and fields](#sections-and-fields)
- [Thresholds and limits](#thresholds-and-limits)
- [OCR and masks](#ocr-and-masks)
- [Filling forms](#filling-forms)
- [Image input formats](#image-input-formats)
- [Tuned weights](#tuned-weights)
- [The model type](#the-model-type)

## Describing an image

One call, no construction:

```go
result, err := mirage.Describe(ctx, "photo.png")
```

Without options the engine picks its default sections; select what you
want explicitly:

```go
result, err := mirage.Describe(ctx, "photo.png",
    mirage.WithSections("objects", "text"),
    mirage.WithFields("bbox", "score"),
)
```

`result` is the engine's structured JSON — the `objects` section, for
instance, is a list of detected objects, each carrying the fields you
selected. The [vision example](../examples/vision/main.go) prints a full
description and fills a form.

## Sections and fields

`WithSections` picks what the description contains; the engine rejects
unknown names:

| Section | Content |
| --- | --- |
| `scene` | The overall scene. |
| `objects` | Detected objects with their fields. |
| `regions` | Notable regions of the image. |
| `text` | OCR lines (see [OCR and masks](#ocr-and-masks)). |
| `relations` | Relations between detected objects. |
| `events` | Events the image depicts. |
| `reasoning` | The model's derivation of the above. |

`WithFields` picks what each object carries:

| Field | Content |
| --- | --- |
| `bbox` | Bounding box. |
| `score` | Detection confidence. |
| `point` / `position` | Location of the object. |
| `description` | A short textual description of the object. |
| `count` | Instance count. |
| `mask` | Segmentation mask (see [OCR and masks](#ocr-and-masks)). |

The detection threshold is the `conf` **setting** (`WithConf`), not a
field — asking for a `conf` field is rejected, exactly as the engine
rejects any unknown name.

## Thresholds and limits

| Option | Effect |
| --- | --- |
| `WithConf(x)` | Detection confidence threshold in `(0,1]`. |
| `WithTextConf(x)` | OCR confidence threshold. |
| `WithMaxObjects(n)` | Cap on detected objects. |
| `WithMaxTextLines(n)` | Cap on OCR text lines. |
| `WithNMS(x)` | Non-maximum-suppression overlap threshold. |
| `WithMaxTokens(n)` | Cap on the response length in tokens. |

`WithLanguage(code)` sets the output language, and `WithKeywords`
biases recognition towards given words and phrases — the same steering
the speech APIs use, applied to what the model writes into `description`
fields and OCR output.

## OCR and masks

The `text` section returns OCR lines; `WithTextConf` trades recall for
precision, and `WithMaxTextLines` bounds the count. Mask geometry is
selected per-object with the `mask` field and shaped by `WithMaskFormat`:

| Format | Encoding |
| --- | --- |
| `largest` | The single largest mask region. |
| `merged` | All mask regions merged. |
| `polygons` | Mask contours as polygons. |
| `bitmap` | A full per-pixel bitmap. |

## Filling forms

`Extract` turns the model loose on an **ordered form** you declare, and
returns the filled fields as a JSON object — the vision counterpart of
Needle's [extraction](extraction.md), grammar-constrained the same way:

```go
form, err := mirage.Extract(ctx, "doc.png", map[string]any{
    "animal":  map[string]any{"enum": []any{"dog", "cat", "bird"}},
    "count":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
    "caption": map[string]any{"type": "string"},
})
// form["animal"] == "dog", form["count"] == 2.0, form["caption"] == "..."
```

Four field kinds are supported:

| Declaration | Meaning |
| --- | --- |
| `{"enum": [...]}` | A choice — the model can only emit one of the values. |
| `{"type": "integer", "minimum": n, "maximum": m}` | A bounded count. |
| `{"type": "string"}` | Free text. |
| `{"x-source": "<field>"}` | Copy another field's box (for cropping pipelines). |

Field order is preserved on the way to the engine (Go maps carry no
order, so the payload is assembled by hand) — order matters because the
model reads fields as an ordered list. The same form can be passed to
`Describe` via `WithForm`, and an empty form is an error.

## Image input formats

Every vision input is normalized to packed RGB through `mirage.ToRGB`:

| Input | Meaning |
| --- | --- |
| `string` | A PNG, JPEG or GIF file path. |
| `[]byte` | Encoded PNG, JPEG or GIF bytes. |
| `image.Image` | Any Go-decoded image. |
| `mirage.RGB` / `*mirage.RGB` | Packed 8-bit RGB: `Width*Height*3` bytes, row-major. |

JPEGs are auto-rotated by their EXIF orientation, the same subset of the
EXIF standard Pillow's `exif_transpose` acts on for the Python reference.
Pass a `*RGB` when you already hold decoded pixels (say, from a camera
stream) to skip a re-decode; `ToRGB` is exported for exactly that
pipeline.

## Tuned weights

Run a tuned `mirage.cact` archive by constructing the model with
`WithWeights`:

```go
vision := mirage.New(mirage.WithWeights("my_mirage.cact"))
```

The archive is validated on first call: a non-vision archive fails with
"the selected archive is not a Mirage vision model" instead of producing
garbage. See [Tuned weights](weights.md) for the worker-process machinery.

## The model type

`mirage.New` normalizes options and never fails — the engine is located
(and downloaded on first use) lazily by the first call. Construction
options:

| Option | Default | Effect |
| --- | --- | --- |
| `WithWeights(path)` | shipped `mirage.cact` | Run a tuned archive. |
| `WithEnginePath(path)` | resolution order | Pin the engine library (see [Engine management](engine.md)). |
| `WithBufferSize(n)` | 16 MiB | JSON output buffer in bytes. |
| `WithLogger(logger)` | — | Log download and engine lifecycle events. |

`WithOptions` merges a raw options dictionary into a call — the escape
hatch for engine settings without a dedicated `With*` function; a key
supplied both there and as an explicit setting is an error. A `Mirage`
serializes its calls internally, and through the shared engine with
Needle and Whistle calls in the same process.

## Where to go next

- The CLI's describe command: [CLI reference](cli.md#describe)
- Runnable code: [vision example](../examples/vision/main.go)
- Symbol-by-symbol: [API reference](api.md#package-cactusmirage)
