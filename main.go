package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ── Types ──────────────────────────────────────────────────────────────────

type analyzeRequest struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

type insights struct {
	FileType  string      `json:"fileType"`
	SizeBytes int64       `json:"sizeBytes"`
	Details   interface{} `json:"details"`
}

type textDetails struct {
	Lines    int    `json:"lines"`
	Words    int    `json:"words"`
	Chars    int    `json:"chars"`
	Language string `json:"language,omitempty"`
}

type csvDetails struct {
	Rows    int                      `json:"rows"`
	Columns int                      `json:"columns"`
	Headers []string                 `json:"headers"`
	Numeric map[string]*numericStats `json:"numericColumns,omitempty"`
}

type numericStats struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

type jsonFileDetails struct {
	RootType     string `json:"rootType"`
	TopLevelKeys int    `json:"topLevelKeys,omitempty"`
	ArrayLength  int    `json:"arrayLength,omitempty"`
	MaxDepth     int    `json:"maxDepth"`
}

type imageDetails struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
}

type binaryDetails struct {
	Magic   string  `json:"magic"`
	Entropy float64 `json:"entropy"`
}

// ── Main ───────────────────────────────────────────────────────────────────

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/healthz", handleHealth)
	http.HandleFunc("/analyze", handleAnalyze)

	log.Printf("analyzer listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req analyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Bucket == "" || req.Key == "" {
		http.Error(w, "bucket and key are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	// Load AWS config — credentials are injected by the CPLN workload identity.
	// The analyzer-identity has S3 read-only access; it never writes.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		http.Error(w, "failed to load AWS config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	client := s3.NewFromConfig(cfg)

	// Detect the actual bucket region to handle cross-region buckets.
	actualRegion, err := manager.GetBucketRegion(ctx, client, req.Bucket)
	if err == nil && actualRegion != "" && actualRegion != region {
		cfg, _ = config.LoadDefaultConfig(ctx, config.WithRegion(actualRegion))
		client = s3.NewFromConfig(cfg)
	}

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(req.Bucket),
		Key:    aws.String(req.Key),
	})
	if err != nil {
		http.Error(w, "failed to fetch from S3: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer result.Body.Close()

	// Read up to 10 MB; large files get partial analysis (still useful for CSV/JSON headers).
	data, err := io.ReadAll(io.LimitReader(result.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "failed to read object: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var sizeBytes int64 = int64(len(data))
	if result.ContentLength != nil {
		sizeBytes = *result.ContentLength
	}

	ext := strings.ToLower(filepath.Ext(req.Key))
	fileType, details := analyze(ext, data)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(insights{
		FileType:  fileType,
		SizeBytes: sizeBytes,
		Details:   details,
	})
}

// ── Analysis dispatch ──────────────────────────────────────────────────────

func analyze(ext string, data []byte) (string, interface{}) {
	// Try image decoding first — works across extensions.
	if img, format, err := image.Decode(bytes.NewReader(data)); err == nil {
		b := img.Bounds()
		return "image/" + format, imageDetails{
			Width:  b.Max.X - b.Min.X,
			Height: b.Max.Y - b.Min.Y,
			Format: format,
		}
	}

	switch ext {
	case ".csv":
		return "text/csv", analyzeCSV(data)
	case ".json":
		return "application/json", analyzeJSON(data)
	case ".txt", ".md", ".log", ".yaml", ".yml", ".toml", ".env",
		".go", ".js", ".ts", ".py", ".rb", ".rs", ".java", ".c",
		".cpp", ".h", ".sh", ".bash", ".dockerfile", ".tf", ".sql":
		return "text/plain", analyzeText(data, extToLanguage(ext))
	default:
		if isText(data) {
			return "text/plain", analyzeText(data, "")
		}
		return "application/octet-stream", binaryDetails{
			Magic:   detectMagic(data),
			Entropy: calcEntropy(data),
		}
	}
}

// ── Text ───────────────────────────────────────────────────────────────────

func analyzeText(data []byte, language string) textDetails {
	text := string(data)
	lines := strings.Count(text, "\n")
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		lines++
	}
	return textDetails{
		Lines:    lines,
		Words:    len(strings.Fields(text)),
		Chars:    len([]rune(text)),
		Language: language,
	}
}

func extToLanguage(ext string) string {
	m := map[string]string{
		".go": "go", ".js": "javascript", ".ts": "typescript",
		".py": "python", ".rb": "ruby", ".rs": "rust",
		".java": "java", ".c": "c", ".cpp": "c++",
		".sh": "shell", ".bash": "shell", ".sql": "sql",
		".yaml": "yaml", ".yml": "yaml", ".toml": "toml",
		".md": "markdown", ".dockerfile": "dockerfile", ".tf": "terraform",
	}
	return m[ext]
}

// ── CSV ────────────────────────────────────────────────────────────────────

func analyzeCSV(data []byte) csvDetails {
	r := csv.NewReader(bytes.NewReader(data))
	records, err := r.ReadAll()
	if err != nil || len(records) == 0 {
		return csvDetails{}
	}

	headers := records[0]
	dataRows := records[1:]

	// Track which columns are entirely numeric.
	numericCols := make(map[string]*numericStats)
	sums := make(map[string]float64)
	counts := make(map[string]int)
	for _, h := range headers {
		numericCols[h] = &numericStats{Min: math.MaxFloat64, Max: -math.MaxFloat64}
	}

	for _, row := range dataRows {
		for i, val := range row {
			if i >= len(headers) {
				break
			}
			h := headers[i]
			if _, still := numericCols[h]; !still {
				continue
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			if err != nil {
				delete(numericCols, h)
				continue
			}
			if f < numericCols[h].Min {
				numericCols[h].Min = f
			}
			if f > numericCols[h].Max {
				numericCols[h].Max = f
			}
			sums[h] += f
			counts[h]++
		}
	}

	result := make(map[string]*numericStats)
	for h, stats := range numericCols {
		if counts[h] > 0 {
			round := func(v float64) float64 { return math.Round(v*100) / 100 }
			result[h] = &numericStats{
				Min:  round(stats.Min),
				Max:  round(stats.Max),
				Mean: round(sums[h] / float64(counts[h])),
			}
		}
	}
	if len(result) == 0 {
		result = nil
	}

	return csvDetails{
		Rows:    len(dataRows),
		Columns: len(headers),
		Headers: headers,
		Numeric: result,
	}
}

// ── JSON ───────────────────────────────────────────────────────────────────

func analyzeJSON(data []byte) jsonFileDetails {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return jsonFileDetails{}
	}
	depth := jsonDepth(v, 0)
	switch val := v.(type) {
	case map[string]interface{}:
		return jsonFileDetails{RootType: "object", TopLevelKeys: len(val), MaxDepth: depth}
	case []interface{}:
		return jsonFileDetails{RootType: "array", ArrayLength: len(val), MaxDepth: depth}
	default:
		return jsonFileDetails{RootType: "scalar"}
	}
}

func jsonDepth(v interface{}, current int) int {
	switch val := v.(type) {
	case map[string]interface{}:
		max := current + 1
		for _, child := range val {
			if d := jsonDepth(child, current+1); d > max {
				max = d
			}
		}
		return max
	case []interface{}:
		max := current + 1
		for _, child := range val {
			if d := jsonDepth(child, current+1); d > max {
				max = d
			}
		}
		return max
	default:
		return current
	}
}

// ── Binary helpers ─────────────────────────────────────────────────────────

func isText(data []byte) bool {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}
	return true
}

func detectMagic(data []byte) string {
	if len(data) < 4 {
		return "unknown"
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF")):
		return "PDF document"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "ZIP archive"
	case bytes.HasPrefix(data, []byte("\x1f\x8b")):
		return "GZIP archive"
	case bytes.HasPrefix(data, []byte("\x7fELF")):
		return "ELF binary"
	case bytes.HasPrefix(data, []byte("BZh")):
		return "BZIP2 archive"
	case bytes.HasPrefix(data, []byte("SQLite format")):
		return "SQLite database"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "JPEG image"
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		return "PNG image"
	case bytes.HasPrefix(data, []byte("GIF8")):
		return "GIF image"
	case bytes.HasPrefix(data, []byte("ID3")):
		return "MP3 audio"
	case len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp")):
		return "MP4 video"
	default:
		return "binary data"
	}
}

func calcEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	freq := make(map[byte]int)
	for _, b := range data {
		freq[b]++
	}
	n := float64(len(data))
	var e float64
	for _, count := range freq {
		p := float64(count) / n
		e -= p * math.Log2(p)
	}
	return math.Round(e*100) / 100
}
