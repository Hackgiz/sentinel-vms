// Package semantic is local image-text search (WAVE16-DETECTION.md,
// stream E): CLIP ViT-B/32 turns an event's whole snapshot and a typed
// description into 512-d unit vectors in one space, so cosine similarity
// says how well the words describe the picture. Nothing leaves the server
// and no AI key is needed.
//
// The models sit behind the Engine interface like the appearance model:
// the server runs without them (semantic search reports why) and tests use
// a fake.
package semantic

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"sync"

	"sentinel-linux/internal/reid"
)

// Dim is the CLIP ViT-B/32 embedding size.
const Dim = 512

// Engine embeds snapshots and descriptions into one space. Implementations
// must be safe for concurrent use.
type Engine interface {
	// Model names the model the vectors come from (the image model file's
	// SHA-256 prefix). Vectors from another model are never compared.
	Model() string
	// EmbedImage returns the L2-normalized vector of a whole snapshot.
	EmbedImage(img image.Image) ([]float32, error)
	// EmbedText returns the L2-normalized vector of a description.
	EmbedText(text string) ([]float32, error)
}

// ImageNet runs the image tower: pixel_values [1, 3, 224, 224] in, the
// projected embedding (image_embeds, [1, 512]) out.
type ImageNet interface {
	Run(pixels []float32) ([]float32, error)
}

// TextNet runs the text tower: input_ids [1, 77] in, the projected
// embedding (text_embeds, [1, 512]) out.
type TextNet interface {
	Run(ids []int64) ([]float32, error)
}

// ErrEmptyImage is a snapshot without pixels.
var ErrEmptyImage = errors.New("snapshot is empty")

// ModelEngine is the production Engine: preprocessing, the two towers and
// L2 normalization.
type ModelEngine struct {
	img   ImageNet
	txt   TextNet
	tok   *Tokenizer
	model string
	imgMu sync.Mutex // one image inference at a time: bounds CPU next to the video work
	txtMu sync.Mutex // text runs separately so a search never waits behind indexing
}

// NewModelEngine wires the towers and the tokenizer under a model id.
func NewModelEngine(img ImageNet, txt TextNet, tok *Tokenizer, model string) (*ModelEngine, error) {
	switch {
	case img == nil:
		return nil, errors.New("no image model")
	case txt == nil:
		return nil, errors.New("no text model")
	case tok == nil:
		return nil, errors.New("no tokenizer")
	case model == "":
		return nil, errors.New("no model id")
	}
	return &ModelEngine{img: img, txt: txt, tok: tok, model: model}, nil
}

func (m *ModelEngine) Model() string { return m.model }

// EmbedImage implements Engine.
func (m *ModelEngine) EmbedImage(img image.Image) ([]float32, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, ErrEmptyImage
	}
	in := Preprocess(img)
	m.imgMu.Lock()
	out, err := m.img.Run(in)
	m.imgMu.Unlock()
	if err != nil {
		return nil, err
	}
	return finish(out)
}

// EmbedText implements Engine.
func (m *ModelEngine) EmbedText(text string) ([]float32, error) {
	ids := m.tok.Tokens(text)
	m.txtMu.Lock()
	out, err := m.txt.Run(ids)
	m.txtMu.Unlock()
	if err != nil {
		return nil, err
	}
	return finish(out)
}

func finish(out []float32) ([]float32, error) {
	if len(out) != Dim {
		return nil, fmt.Errorf("model output has %d values; expected one %d-d embedding", len(out), Dim)
	}
	return reid.Normalize(append([]float32(nil), out...))
}

// Verify wires the towers under id and proves both on a warm-up (a gray
// snapshot and a short description) before any event uses them: each must
// give a finite, non-zero 512-d vector. On failure closer (if any) runs and
// the reason comes back.
func Verify(img ImageNet, txt TextNet, tok *Tokenizer, id string, closer func()) (Engine, string) {
	fail := func(msg string) (Engine, string) {
		if closer != nil {
			closer()
		}
		return nil, msg
	}
	m, err := NewModelEngine(img, txt, tok, id)
	if err != nil {
		return fail(err.Error())
	}
	gray := image.NewUniform(color.Gray{Y: 128})
	if _, err := m.EmbedImage(&boundedImage{gray, image.Rect(0, 0, ImageSize, ImageSize)}); err != nil {
		return fail("semantic image model failed a test run: " + err.Error())
	}
	if _, err := m.EmbedText("a photo of a street"); err != nil {
		return fail("semantic text model failed a test run: " + err.Error())
	}
	return m, ""
}

// boundedImage gives an infinite image (image.Uniform) finite bounds.
type boundedImage struct {
	image.Image
	r image.Rectangle
}

func (b *boundedImage) Bounds() image.Rectangle { return b.r }

// Cosine is the cosine similarity of two unit vectors.
func Cosine(a, b []float32) float64 { return reid.Cosine(a, b) }
