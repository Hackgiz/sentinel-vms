package reid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"

	"sentinel-linux/internal/ort"

	"sentinel-linux/internal/modelpath"
)

// ModelEnv names the model file; without it <data>/models/reid.onnx is used.
const ModelEnv = "SENTINEL_REID_MODEL"

// ModelPath returns where the optional re-identification model is expected
// and whether the file exists.
func ModelPath(dataDir string) (path string, exists bool) {
	path = os.Getenv(ModelEnv)
	if path == "" {
		path = modelpath.Find(dataDir, "reid.onnx")
	}
	fi, err := os.Stat(path)
	return path, err == nil && fi.Mode().IsRegular()
}

// ModelID is the model's identity in event_embeddings.model: the first 16
// hex digits of the file's SHA-256, so replacing the file (even under the
// same name) invalidates every stored vector.
func ModelID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// onnxNet runs an OSNet-class ONNX network through ONNX Runtime (the same
// purego binding object detection uses). WAVE9(A): TODO confirm against a
// real export: this feeds one float32 tensor [1, 3, 256, 128] (RGB, CHW,
// ImageNet-normalized) to the model's first input and reads its first
// output, flattened, as the embedding (OSNet x0.25..x1.0: [1, 512]).
type onnxNet struct{ sess *ort.Session }

func (o *onnxNet) Run(input []float32) ([]float32, error) {
	out, _, err := o.sess.Run(input, []int64{1, 3, InputH, InputW})
	return out, err
}

// Open loads the optional model. It returns (nil, reason) when appearance
// search is unavailable (no model file, no ONNX Runtime, a model that fails
// to load or run); the caller surfaces the reason. It never panics on a bad
// model.
func Open(dataDir, ortLib string) (e Embedder, reason string) {
	defer func() {
		if r := recover(); r != nil {
			e, reason = nil, fmt.Sprintf("appearance model failed to start: %v", r)
		}
	}()
	model, ok := ModelPath(dataDir)
	if !ok {
		return nil, "no appearance model installed (" + model + ")"
	}
	if ortLib == "" {
		return nil, "ONNX Runtime library not found"
	}
	id, err := ModelID(model)
	if err != nil {
		return nil, "appearance model could not be read: " + err.Error()
	}
	rt, err := ort.Load(ortLib)
	if err != nil {
		return nil, "ONNX Runtime unavailable: " + err.Error()
	}
	sess, err := rt.NewSession(model, 1)
	if err != nil {
		return nil, "appearance model could not be loaded: " + err.Error()
	}
	return Verify(&onnxNet{sess: sess}, id, sess.Close)
}

// Verify wires net under id and proves it on a warm-up crop (a plain gray
// person-shaped image) before any event uses it: the output must be a
// finite, non-zero vector of a sane length. On failure closer (if any) runs
// and the reason comes back.
func Verify(net Net, id string, closer func()) (Embedder, string) {
	fail := func(msg string) (Embedder, string) {
		if closer != nil {
			closer()
		}
		return nil, msg
	}
	m, err := NewModelEmbedder(net, id)
	if err != nil {
		return fail(err.Error())
	}
	warm := image.NewUniform(color.Gray{Y: 128})
	if _, err := m.Embed(&boundedImage{warm, image.Rect(0, 0, InputW, InputH)}); err != nil {
		return fail("appearance model failed a test run: " + err.Error())
	}
	return m, ""
}

// boundedImage gives an infinite image (image.Uniform) finite bounds.
type boundedImage struct {
	image.Image
	r image.Rectangle
}

func (b *boundedImage) Bounds() image.Rectangle { return b.r }
