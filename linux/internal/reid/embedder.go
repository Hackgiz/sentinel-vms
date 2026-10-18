package reid

import (
	"errors"
	"fmt"
	"image"
	"sync"
)

// Embedder turns one detection crop into a unit-length appearance vector.
// It is the seam the ONNX model sits behind; tests use a fake.
// Implementations must be safe for concurrent use.
type Embedder interface {
	// Model names the model the vectors come from (the model file's SHA-256
	// prefix). Vectors from different models are never compared: a model
	// swap invalidates the index.
	Model() string
	// Embed returns the L2-normalized vector of a crop.
	Embed(crop image.Image) ([]float32, error)
}

// Net runs the network: one preprocessed input (1×3×InputH×InputW) in, the
// raw output tensor (any shape, read as one flat vector) out.
type Net interface {
	Run(input []float32) ([]float32, error)
}

// MaxDim bounds the vector length accepted from a model (OSNet: 512).
const MaxDim = 4096

// ModelEmbedder is the production Embedder: Preprocess, a Net, then L2
// normalization.
type ModelEmbedder struct {
	net   Net
	model string
	sem   sync.Mutex // one inference at a time: bounds CPU next to the video work
}

// NewModelEmbedder wires a network under a model id.
func NewModelEmbedder(net Net, model string) (*ModelEmbedder, error) {
	if net == nil {
		return nil, errors.New("no model")
	}
	if model == "" {
		return nil, errors.New("no model id")
	}
	return &ModelEmbedder{net: net, model: model}, nil
}

func (m *ModelEmbedder) Model() string { return m.model }

// Embed implements Embedder.
func (m *ModelEmbedder) Embed(crop image.Image) ([]float32, error) {
	if crop == nil || crop.Bounds().Empty() {
		return nil, ErrTooSmall
	}
	in := Preprocess(crop)
	m.sem.Lock()
	out, err := m.net.Run(in)
	m.sem.Unlock()
	if err != nil {
		return nil, err
	}
	if len(out) > MaxDim {
		return nil, fmt.Errorf("model output has %d values; expected one embedding of at most %d", len(out), MaxDim)
	}
	v := append([]float32(nil), out...)
	return Normalize(v)
}
