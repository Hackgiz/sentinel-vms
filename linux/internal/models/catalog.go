// Package models is the catalog of every model file Sentinel can use
// (WAVE16-DETECTION.md, stream A): the bundled detector, the downloadable
// detector tiers and the optional models behind appearance search, audio
// classification, faces, plates and semantic search. It reports each one's
// install state under <data>/models, downloads them with SHA-256
// verification, and describes a user-supplied custom detector.
//
// Nothing here loads a model: each feature opens its own file by the
// catalog name (with its environment override) and runs as before when the
// file is missing.
package models

import (
	"path/filepath"
)

// Purposes (Model.purpose in the API).
const (
	PurposeDetector   = "detector"
	PurposeReID       = "reid"
	PurposeAudio      = "audio"
	PurposeFaceDetect = "face-detect"
	PurposeFaceEmbed  = "face-embed"
	PurposePlateDet   = "plate-det"
	PurposePlateRec   = "plate-rec"
	PurposeCLIPImage  = "clip-image"
	PurposeCLIPText   = "clip-text"
)

// Catalog ids every stream refers to.
const (
	IDYOLOXTiny = "yolox-tiny" // bundled; the default detector ("" means this)
	IDYOLOXS    = "yolox-s"
	IDReID      = "reid-osnet"
	IDAudio     = "audio-yamnet"
	IDFaceDet   = "face-yunet"
	IDFaceRec   = "face-sface"
	IDPlateDet  = "plate-det"
	IDPlateRec  = "plate-rec"
	IDCLIPImage = "clip-image"
	IDCLIPText  = "clip-text"
	IDCustom    = "custom" // custom.onnx + custom.json, supplied by hand
)

// File is one file of a model.
type File struct {
	Name   string // under the models directory
	URL    string // pinned download URL ("" = bundled or hand-installed)
	SHA256 string // hex
	Size   int64  // bytes
}

// Entry is one catalog model. File, SizeBytes and SHA256 describe the
// first file (SizeBytes: all files); Files lists every file, and the model
// is installed only when all of them are.
type Entry struct {
	ID        string
	Title     string
	Purpose   string
	File      string
	SizeBytes int64
	SHA256    string
	License   string
	Source    string // where it comes from (URL of the first file, or a page)
	Note      string
	Bundled   bool
	Files     []File
	// Input is a detector's square input size; Family its decoder
	// (detect.FamilyYOLOX / FamilyUltralytics). Detectors only.
	Input  int
	Family string
}

func entry(e Entry) Entry {
	if len(e.Files) > 0 {
		e.File = e.Files[0].Name
		e.SHA256 = e.Files[0].SHA256
		if e.Source == "" {
			e.Source = e.Files[0].URL
		}
	}
	e.SizeBytes = 0
	for _, f := range e.Files {
		e.SizeBytes += f.Size
	}
	return e
}

// Dir is the models directory under the data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, "models") }

// Catalog is every model Sentinel can use, in display order. The SHA-256s
// and sizes were recorded by downloading each file (2026-10-21).
func Catalog() []Entry {
	return []Entry{
		entry(Entry{
			ID: IDYOLOXTiny, Title: "YOLOX-Tiny (416, fast)", Purpose: PurposeDetector,
			License: "Apache-2.0", Bundled: true, Input: 416, Family: "yolox",
			Source: "https://github.com/Megvii-BaseDetection/YOLOX/releases/tag/0.1.1rc0",
			Note:   "Bundled person, vehicle and animal detector. Runs on any CPU at one pass a second per camera.",
			Files: []File{{Name: "yolox_tiny.onnx",
				URL:    "https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_tiny.onnx",
				SHA256: "427cc366d34e27ff7a03e2899b5e3671425c262ea2291f88bb942bc1cc70b0f7", Size: 20219662}},
		}),
		entry(Entry{
			ID: IDYOLOXS, Title: "YOLOX-S (640, more accurate)", Purpose: PurposeDetector,
			License: "Apache-2.0", Input: 640, Family: "yolox",
			Note: "Finds smaller and farther objects than YOLOX-Tiny. About 6× the work per frame: best with an Intel GPU, NPU or Apple silicon.",
			Files: []File{{Name: "yolox_s.onnx",
				URL:    "https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_s.onnx",
				SHA256: "c5c2d13e59ae883e6af3b45daea64af4833a4951c92d116ec270d9ddbe998063", Size: 35858002}},
		}),
		entry(Entry{
			// No public OSNet ONNX export takes batch 1 (kornia/osnet's
			// osnet_x0_25_msmt17.onnx is fixed at 16): installed by hand, as
			// FIELD-VALIDATION.md describes. No URL, so no download button.
			ID: IDReID, Title: "OSNet (appearance search)", Purpose: PurposeReID,
			License: "MIT",
			Note:    "Re-identification model for \"Find similar\", installed by hand: an OSNet ONNX export, input 1×3×256×128 RGB with ImageNet normalization, output the 512-d feature.",
			Files:   []File{{Name: "reid.onnx"}},
		}),
		entry(Entry{
			ID: IDAudio, Title: "YAMNet (audio classes)", Purpose: PurposeAudio,
			License: "Apache-2.0",
			Note:    "Sound classifier for glass break, scream, gunshot, alarm, dog bark, car horn, siren and baby cry.",
			Files: []File{{Name: "audio.onnx",
				URL:    "https://huggingface.co/audiomagic/yamnet-onnx/resolve/main/yamnet.onnx",
				SHA256: "d3835ffbbd4a1bb3e777f0ca217b5007907f5171dd5d17c4236b95b2af8f908e", Size: 16093355}},
		}),
		entry(Entry{
			ID: IDFaceDet, Title: "YuNet (face detection)", Purpose: PurposeFaceDetect,
			License: "Apache-2.0",
			Note:    "Finds faces and their landmarks in Person snapshots. Needed for face recognition, with SFace.",
			Files: []File{{Name: "face_det.onnx",
				URL:    "https://github.com/opencv/opencv_zoo/raw/48b10fa505aeb51fbd127fc60a5c660040e823b7/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
				SHA256: "8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4", Size: 232589}},
		}),
		entry(Entry{
			ID: IDFaceRec, Title: "SFace (face recognition)", Purpose: PurposeFaceEmbed,
			License: "Apache-2.0",
			Note:    "Turns an aligned face into a 128-d vector to match against known people. Needed for face recognition, with YuNet.",
			Files: []File{{Name: "face_rec.onnx",
				URL:    "https://github.com/opencv/opencv_zoo/raw/48b10fa505aeb51fbd127fc60a5c660040e823b7/models/face_recognition_sface/face_recognition_sface_2021dec.onnx",
				SHA256: "0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79", Size: 38696353}},
		}),
		entry(Entry{
			ID: IDPlateDet, Title: "PP-OCRv3 text detection (plates)", Purpose: PurposePlateDet,
			License: "Apache-2.0",
			Note:    "Finds the text on a vehicle crop. Needed for plate reading, with the recognizer.",
			Files: []File{{Name: "plate_det.onnx",
				URL:    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv4/det/en_PP-OCRv3_det_mobile.onnx",
				SHA256: "ea07c15d38ac40cd69da3c493444ec75b44ff23840553ff8ba102c1219ed39c2", Size: 2421707}},
		}),
		entry(Entry{
			ID: IDPlateRec, Title: "PP-OCRv4 text recognition (plates)", Purpose: PurposePlateRec,
			License: "Apache-2.0",
			Note:    "Reads the plate characters (English letters and digits). Needed for plate reading, with the detector.",
			Files: []File{
				{Name: "plate_rec.onnx",
					URL:    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv4/rec/en_PP-OCRv4_rec_mobile.onnx",
					SHA256: "e8770c967605983d1570cdf5352041dfb68fa0c21664f49f47b155abd3e0e318", Size: 7653044},
				{Name: "plate_rec.txt",
					URL:    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/paddle/PP-OCRv4/rec/en_PP-OCRv4_rec_mobile/en_dict.txt",
					SHA256: "5662df9d2d03f0e8ca0d3b0649d6acbab904b6a14b3d3521463c71c37c668ce3", Size: 190},
			},
		}),
		entry(Entry{
			ID: IDCLIPImage, Title: "CLIP ViT-B/32 image encoder", Purpose: PurposeCLIPImage,
			License: "MIT",
			Note:    "Describes each snapshot as a vector for \"Describe it\" search. Needed for semantic search, with the text encoder.",
			Files: []File{{Name: "clip_image.onnx",
				URL:    "https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main/onnx/vision_model.onnx",
				SHA256: "fd6e1402a588279d1723c7534d4bcba5bc0b14b47dfab0e46f8c47b8270d7d40", Size: 351685709}},
		}),
		entry(Entry{
			ID: IDCLIPText, Title: "CLIP ViT-B/32 text encoder", Purpose: PurposeCLIPText,
			License: "MIT",
			Note:    "Turns a search phrase into a vector. Needed for semantic search, with the image encoder.",
			Files: []File{
				{Name: "clip_text.onnx",
					URL:    "https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main/onnx/text_model.onnx",
					SHA256: "3f6571f5bad13a97c469c1622e1cfc4d9aef78b79fdbfcff804ca357bfada8cc", Size: 254058553},
				{Name: "clip_vocab.json",
					URL:    "https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main/vocab.json",
					SHA256: "5047b556ce86ccaf6aa22b3ffccfc52d391ea4accdab9c2f2407da5b742d4363", Size: 862328},
				{Name: "clip_merges.txt",
					URL:    "https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main/merges.txt",
					SHA256: "9fd691f7c8039210e0fced15865466c65820d09b63988b0174bfe25de299051a", Size: 524619},
			},
		}),
		entry(Entry{
			ID: IDCustom, Title: "Custom detector", Purpose: PurposeDetector,
			License: "as supplied",
			Note:    "Your own detector: put custom.onnx and custom.json ({\"family\": \"yolox\" or \"ultralytics\", \"input\": 640, \"labels\": {\"0\": \"person\"}}) in the models directory.",
			Files:   []File{{Name: CustomModelFile}, {Name: CustomSpecFile}},
		}),
	}
}

// Lookup returns the catalog entry with id.
func Lookup(id string) (Entry, bool) {
	for _, e := range Catalog() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Downloadable reports whether every file of e has a pinned URL and
// SHA-256 (bundled and hand-installed models have not).
func (e Entry) Downloadable() bool {
	if e.Bundled || len(e.Files) == 0 {
		return false
	}
	for _, f := range e.Files {
		if f.URL == "" || f.SHA256 == "" {
			return false
		}
	}
	return true
}

// IsDetector reports whether id names a detector model ("" = yolox-tiny).
func IsDetector(id string) bool {
	if id == "" {
		return true
	}
	e, ok := Lookup(id)
	return ok && e.Purpose == PurposeDetector
}

// NormDetector maps "" to the bundled detector's id.
func NormDetector(id string) string {
	if id == "" {
		return IDYOLOXTiny
	}
	return id
}
