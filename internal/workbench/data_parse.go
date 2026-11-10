package workbench

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDataBytes         = 16 << 20
	maxDataFiles         = 1000
	maxDataRows          = 100000
	maxDataColumns       = 128
	maxDataRowsJSONBytes = maxDataBytes*16 + 1<<20
	maxDataMetadataBytes = 8 << 20
	maxDataImagePixels   = 16 << 20
)

var dataDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var dataRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type dataRecord map[string]string
type decodedDataFile struct {
	name    string
	content []byte
}

func safeDataPath(name string) bool {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00\r\n") || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return false
		}
	}
	return true
}

func decodeDataFiles(files []DataFile) ([]decodedDataFile, string, int, error) {
	if len(files) == 0 || len(files) > maxDataFiles {
		return nil, "", 0, errors.New("import requires 1 to 1,000 files")
	}
	decoded := make([]decodedDataFile, 0, len(files))
	seen := map[string]bool{}
	total := 0
	for _, file := range files {
		if !safeDataPath(file.Name) || seen[file.Name] {
			return nil, "", 0, fmt.Errorf("unsafe or duplicate file name %q", file.Name)
		}
		seen[file.Name] = true
		if len(file.ContentBase64) > base64.StdEncoding.EncodedLen(maxDataBytes-total)+4 {
			return nil, "", 0, errors.New("import exceeds 16 MiB decoded size limit")
		}
		content, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
		if err != nil {
			return nil, "", 0, fmt.Errorf("%s: invalid base64 content", file.Name)
		}
		total += len(content)
		if total > maxDataBytes {
			return nil, "", 0, errors.New("import exceeds 16 MiB decoded size limit")
		}
		if len(content) == 0 {
			return nil, "", 0, fmt.Errorf("%s is empty", file.Name)
		}
		decoded = append(decoded, decodedDataFile{file.Name, content})
	}
	sort.Slice(decoded, func(i, j int) bool { return decoded[i].name < decoded[j].name })
	h := sha256.New()
	var length [8]byte
	for _, file := range decoded {
		binary.BigEndian.PutUint64(length[:], uint64(len(file.name)))
		h.Write(length[:])
		h.Write([]byte(file.name))
		binary.BigEndian.PutUint64(length[:], uint64(len(file.content)))
		h.Write(length[:])
		h.Write(file.content)
	}
	return decoded, hex.EncodeToString(h.Sum(nil)), total, nil
}

func normalizeDataSource(source DataSource, files []decodedDataFile) (DataSource, error) {
	if source.Kind != "local" && source.Kind != "huggingface" && source.Kind != "example" {
		return source, errors.New("source kind must be local, huggingface, or example")
	}
	source.Name = strings.TrimSpace(source.Name)
	if source.Name == "" {
		source.Name = path.Base(files[0].name)
	}
	if len(source.Name) > 256 || strings.ContainsAny(source.Name, "\x00\r\n") {
		return source, errors.New("source name is invalid")
	}
	if source.Kind == "local" {
		source.Repo = ""
		source.Revision = ""
		source.Files = nil
		return source, nil
	}
	if !dataRepo.MatchString(source.Repo) || !regexp.MustCompile(`^[a-fA-F0-9]{40,64}$`).MatchString(source.Revision) {
		return source, errors.New("Hugging Face imports require a repository and pinned commit revision")
	}
	source.Files = make([]string, len(files))
	for i, file := range files {
		source.Files[i] = file.name
	}
	return source, nil
}

func parseData(files []decodedDataFile) (string, []string, []dataRecord, error) {
	format := ""
	var names []string
	rows := make([]dataRecord, 0)
	for _, file := range files {
		ext := strings.ToLower(path.Ext(file.name))
		current := ""
		switch ext {
		case ".csv", ".tsv", ".jsonl", ".ndjson":
			current = "table"
		case ".txt", ".text":
			current = "text"
		case ".png", ".jpg", ".jpeg":
			current = "images"
		default:
			return "", nil, nil, fmt.Errorf("%s: unsupported format; choose CSV, TSV, flat JSONL, text, PNG, or JPEG", file.name)
		}
		if format != "" && format != current {
			return "", nil, nil, errors.New("cannot mix table, text, and image files in one import")
		}
		format = current
		var cols []string
		var part []dataRecord
		var err error
		switch {
		case current == "images":
			cols = []string{"image", "label", "width", "height", "channels"}
			cfg, kind, e := image.DecodeConfig(bytes.NewReader(file.content))
			if e != nil || (kind != "png" && kind != "jpeg") || cfg.Width <= 0 || cfg.Height <= 0 {
				return "", nil, nil, fmt.Errorf("%s: invalid PNG or JPEG image", file.name)
			}
			if int64(cfg.Width)*int64(cfg.Height) > maxDataImagePixels {
				return "", nil, nil, fmt.Errorf("%s: images may contain at most 16 megapixels", file.name)
			}
			if _, _, e := image.Decode(bytes.NewReader(file.content)); e != nil {
				return "", nil, nil, fmt.Errorf("%s: malformed or truncated image", file.name)
			}
			channels := 3
			if cfg.ColorModel == color.GrayModel || cfg.ColorModel == color.Gray16Model {
				channels = 1
			}
			if cfg.ColorModel == color.NRGBAModel || cfg.ColorModel == color.NRGBA64Model || cfg.ColorModel == color.RGBAModel || cfg.ColorModel == color.RGBA64Model {
				channels = 4
			}
			label := ""
			if dir := path.Dir(file.name); dir != "." {
				label = path.Base(dir)
			}
			part = []dataRecord{{"image": file.name, "label": label, "width": strconv.Itoa(cfg.Width), "height": strconv.Itoa(cfg.Height), "channels": strconv.Itoa(channels)}}
		case current == "text":
			cols = []string{"text"}
			part, err = parseTextData(file.content)
		case ext == ".csv" || ext == ".tsv":
			cols, part, err = parseDelimitedData(file.content, ext == ".tsv")
		default:
			cols, part, err = parseJSONLinesData(file.content)
		}
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s: %w", file.name, err)
		}
		if names == nil {
			names = cols
		} else if strings.Join(names, "\x00") != strings.Join(cols, "\x00") {
			return "", nil, nil, errors.New("all files must use the same columns")
		}
		if len(rows)+len(part) > maxDataRows {
			return "", nil, nil, errors.New("dataset exceeds 100,000 rows")
		}
		rows = append(rows, part...)
	}
	if len(rows) == 0 {
		return "", nil, nil, errors.New("dataset contains no rows")
	}
	return format, names, rows, nil
}

func dataText(content []byte) ([]byte, error) {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("text data must be valid UTF-8 without NUL bytes")
	}
	return content, nil
}

func parseDelimitedData(content []byte, tsv bool) ([]string, []dataRecord, error) {
	content, err := dataText(content)
	if err != nil {
		return nil, nil, err
	}
	r := csv.NewReader(bytes.NewReader(content))
	if tsv {
		r.Comma = '\t'
	}
	names, err := r.Read()
	if err != nil {
		return nil, nil, errors.New("invalid or empty header")
	}
	if len(names) > maxDataColumns {
		return nil, nil, errors.New("dataset exceeds 128 columns")
	}
	seen := map[string]bool{}
	for i, name := range names {
		names[i] = strings.TrimSpace(name)
		if names[i] == "" || seen[names[i]] || len(names[i]) > 256 {
			return nil, nil, errors.New("column names must be nonempty, unique, and at most 256 bytes")
		}
		seen[names[i]] = true
	}
	rows := make([]dataRecord, 0)
	for {
		values, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("invalid or ragged row: %w", err)
		}
		row := dataRecord{}
		for i, value := range values {
			if nonfiniteDataValue(value) {
				return nil, nil, errors.New("nonfinite numeric value")
			}
			row[names[i]] = value
		}
		rows = append(rows, row)
		if len(rows) > maxDataRows {
			return nil, nil, errors.New("dataset exceeds 100,000 rows")
		}
	}
	return names, rows, nil
}

func parseJSONLinesData(content []byte) ([]string, []dataRecord, error) {
	content, err := dataText(content)
	if err != nil {
		return nil, nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 4096), maxDataBytes+1)
	rows := make([]dataRecord, 0)
	keys := map[string]bool{}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		token, e := dec.Token()
		if e != nil || token != json.Delim('{') {
			return nil, nil, errors.New("JSONL rows must be flat objects")
		}
		row := dataRecord{}
		for dec.More() {
			keyToken, e := dec.Token()
			key, ok := keyToken.(string)
			if e != nil || !ok || strings.TrimSpace(key) == "" || len(key) > 256 {
				return nil, nil, errors.New("invalid JSONL column name")
			}
			if _, exists := row[key]; exists {
				return nil, nil, errors.New("duplicate JSONL key")
			}
			var value any
			if e = dec.Decode(&value); e != nil {
				return nil, nil, e
			}
			var cell string
			switch v := value.(type) {
			case nil:
			case string:
				cell = v
			case bool:
				cell = strconv.FormatBool(v)
			case json.Number:
				cell = string(v)
			default:
				return nil, nil, errors.New("nested JSONL values are unsupported")
			}
			if nonfiniteDataValue(cell) {
				return nil, nil, errors.New("nonfinite numeric value")
			}
			row[key] = cell
			keys[key] = true
			if len(keys) > maxDataColumns {
				return nil, nil, errors.New("dataset exceeds 128 columns")
			}
		}
		if _, e = dec.Token(); e != nil {
			return nil, nil, e
		}
		if _, e = dec.Token(); e != io.EOF {
			return nil, nil, errors.New("unexpected content after JSONL object")
		}
		if len(row) == 0 {
			return nil, nil, errors.New("empty JSONL object")
		}
		rows = append(rows, row)
		if len(rows) > maxDataRows {
			return nil, nil, errors.New("dataset exceeds 100,000 rows")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	return names, rows, nil
}

func parseTextData(content []byte) ([]dataRecord, error) {
	content, err := dataText(content)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 4096), maxDataBytes+1)
	rows := make([]dataRecord, 0)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, dataRecord{"text": line})
		if len(rows) > maxDataRows {
			return nil, errors.New("dataset exceeds 100,000 rows")
		}
	}
	return rows, scanner.Err()
}

func nonfiniteDataValue(value string) bool {
	v, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return math.IsNaN(v) || math.IsInf(v, 0)
}

func dataCellPreview(value string) string {
	count := 0
	for i := range value {
		if count == 256 {
			return value[:i]
		}
		count++
	}
	return value
}

func describeData(id string, source DataSource, format string, names []string, rows []dataRecord, bytes int) Dataset {
	ds := Dataset{ID: id, Name: source.Name, Source: source, Format: format, RowCount: len(rows), ByteCount: bytes, Columns: make([]DataColumn, 0, len(names)), Preview: make([]map[string]string, 0, min(len(rows), 20)), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, name := range names {
		col := DataColumn{Name: name, Kind: "number", Examples: []string{}}
		unique := map[string]bool{}
		nonempty := 0
		for _, row := range rows {
			value := row[name]
			if strings.TrimSpace(value) == "" {
				col.Missing++
				continue
			}
			nonempty++
			if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
				col.Kind = "text"
			}
			if !unique[value] {
				unique[value] = true
				if len(col.Examples) < 3 {
					col.Examples = append(col.Examples, dataCellPreview(value))
				}
			}
		}
		col.UniqueCount = len(unique)
		if nonempty == 0 {
			col.Kind = "text"
		}
		if format == "images" && name == "image" {
			col.Kind = "image"
		}
		ds.Columns = append(ds.Columns, col)
	}
	for _, row := range rows[:min(20, len(rows))] {
		preview := map[string]string{}
		for _, name := range names {
			preview[name] = dataCellPreview(row[name])
		}
		ds.Preview = append(ds.Preview, preview)
	}
	return ds
}
