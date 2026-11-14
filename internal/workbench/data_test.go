package workbench

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func dataFixture(t *testing.T, task string) (string, string) {
	t.Helper()
	root := t.TempDir()
	m, err := Create(root, "Data Model", task)
	if err != nil {
		t.Fatal(err)
	}
	return root, m.ID
}

func upload(name, content string) DataFile {
	return DataFile{Name: name, ContentBase64: base64.StdEncoding.EncodeToString([]byte(content))}
}

func TestDataEmptyStateIsExplicit(t *testing.T) {
	root, id := dataFixture(t, "custom")
	got, err := ReadData(root, id, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if string(encoded) != `{"datasets":[],"dataset":null,"selection":null}` {
		t.Fatalf("state = %s", encoded)
	}
}

func TestDataTextContainingInfinityIsNotANumericError(t *testing.T) {
	for _, file := range []DataFile{
		upload("phrases.csv", "text,label\nTo infinity and beyond,positive\nAn infinity symbol,neutral\n"),
		upload("phrases.jsonl", "{\"text\":\"To infinity and beyond\",\"label\":\"positive\"}\n"),
	} {
		root, id := dataFixture(t, "classification")
		ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{file})
		if err != nil {
			t.Fatalf("valid text rejected: %v", err)
		}
		if ds.Preview[0]["text"] != "To infinity and beyond" {
			t.Fatalf("text changed: %+v", ds.Preview)
		}
	}
}

func TestDataIdentifierThatLooksLikeOverflowingNumberImportsAsText(t *testing.T) {
	for _, file := range []DataFile{
		upload("train.csv", "id,text,model\n251e20802938,first,a\nabcd1234,second,b\n"),
		upload("train.jsonl", "{\"id\":\"251e20802938\",\"text\":\"first\",\"model\":\"a\"}\n{\"id\":\"abcd1234\",\"text\":\"second\",\"model\":\"b\"}\n"),
	} {
		t.Run(file.Name, func(t *testing.T) {
			root, id := dataFixture(t, "classification")
			ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{file})
			if err != nil {
				t.Fatalf("identifier rejected: %v", err)
			}
			if ds.Preview[0]["id"] != "251e20802938" || ds.Columns[0].Kind != "text" {
				t.Fatalf("identifier changed or misclassified: %+v", ds)
			}
		})
	}
}

func TestDataPlainTextNaNIsNotNumericData(t *testing.T) {
	root, id := dataFixture(t, "text-generation")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("notes.txt", "NaN\n")})
	if err != nil {
		t.Fatalf("plain text rejected: %v", err)
	}
	if ds.Preview[0]["text"] != "NaN" {
		t.Fatalf("plain text changed: %+v", ds.Preview)
	}
}

func TestDataWarnsWhenStratificationLeavesRequestedHoldoutEmpty(t *testing.T) {
	root, id := dataFixture(t, "classification")
	var rows strings.Builder
	rows.WriteString("x,label\n")
	for class := 0; class < 10; class++ {
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&rows, "%d,class-%d\n", i, class)
		}
	}
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("classes.csv", rows.String())})
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", Scale: "none", TrainPercent: 80, ValidationPercent: 10, TestPercent: 10, Seed: 42, Stratify: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Test == 0 && !strings.Contains(strings.ToLower(strings.Join(result.Warnings, " ")), "test split") {
		t.Fatal("requested test split is empty without an actionable warning")
	}
}

// Losing CSV quoting, BOM handling, or decoded values must change this preview.
func TestImportDataParsesAndPersistsActualRows(t *testing.T) {
	for _, tc := range []struct {
		name, content, format, first string
		columns                      int
	}{
		{"rows.csv", "\ufeffname,value,label\n\"hello, world\",1,yes\n\"two\nlines\",,no\n", "table", "hello, world", 3},
		{"rows.tsv", "name\tvalue\tlabel\nhello\t1\tyes\nsecond\t2\tno\n", "table", "hello", 3},
		{"rows.jsonl", "{\"name\":\"hello\",\"value\":1,\"label\":\"yes\"}\n{\"name\":\"second\",\"value\":null,\"label\":\"no\"}\n", "table", "hello", 3},
		{"rows.txt", "hello\nsecond\n", "text", "hello", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, id := dataFixture(t, "custom")
			ds, err := ImportData(root, id, DataSource{Kind: "local", Name: "My rows"}, []DataFile{upload(tc.name, tc.content)})
			if err != nil {
				t.Fatal(err)
			}
			if ds.Name != "My rows" || ds.Format != tc.format || ds.RowCount != 2 || ds.ByteCount != len(tc.content) || len(ds.Columns) != tc.columns {
				t.Fatalf("metadata = %+v", ds)
			}
			key := "name"
			if tc.format == "text" {
				key = "text"
			}
			if ds.Preview[0][key] != tc.first {
				t.Fatalf("preview = %#v", ds.Preview)
			}
			state, err := ReadData(root, id, ds.ID)
			if err != nil || state.Dataset == nil || !reflect.DeepEqual(*state.Dataset, ds) || len(state.Datasets) != 1 || len(state.Datasets[0].Preview) != 0 {
				t.Fatalf("reopen = %+v, %v", state, err)
			}
			again, err := ImportData(root, id, DataSource{Kind: "local", Name: "A renamed import"}, []DataFile{upload(tc.name, tc.content)})
			if err != nil || !reflect.DeepEqual(ds, again) {
				t.Fatalf("duplicate = %+v, %v", again, err)
			}
			if tc.name == "rows.csv" && (ds.Columns[1].Kind != "number" || ds.Columns[1].Missing != 1 || ds.Columns[0].UniqueCount != 2) {
				t.Fatalf("columns = %+v", ds.Columns)
			}
		})
	}
}

func TestImportDataImageFoldersAndBoundedPreview(t *testing.T) {
	root, id := dataFixture(t, "custom")
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.White)
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{{Name: "cats/a.png", ContentBase64: base64.StdEncoding.EncodeToString(b.Bytes())}})
	if err != nil {
		t.Fatal(err)
	}
	if ds.Format != "images" || ds.Preview[0]["label"] != "cats" || ds.Preview[0]["width"] != "3" || ds.Preview[0]["height"] != "2" || ds.Columns[0].Kind != "image" {
		t.Fatalf("image metadata = %+v", ds)
	}
	content := "text,label\n" + strings.Repeat(strings.Repeat("界", 300)+",yes\n", 25)
	ds, err = ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("preview.csv", content)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Preview) != 20 || len([]rune(ds.Preview[0]["text"])) != 256 {
		t.Fatalf("unbounded preview = %d rows/%d runes", len(ds.Preview), len([]rune(ds.Preview[0]["text"])))
	}
}

func TestImportDataRejectsInvalidWithoutChangingPriorData(t *testing.T) {
	root, id := dataFixture(t, "custom")
	good, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("ok.csv", "x,y\n1,yes\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, content string }{
		{"empty.csv", "x,y\n"}, {"ragged.csv", "x,y\n1,2,3\n"}, {"duplicate.csv", "x,x\n1,2\n"},
		{"bad.jsonl", "{\"x\":{\"nested\":1}}\n"}, {"bad.jsonl", "{\"x\":1}\ninvalid\n"},
		{"nan.csv", "x,y\nNaN,yes\n"}, {"infinity.csv", "x,y\n1e999,yes\n"}, {"empty.txt", " \n\n"},
		{"script.py", "print('no')"}, {"archive.zip", "zip"}, {"image.png", "bad png"},
		{"../escape.txt", "hello"}, {"/absolute.txt", "hello"}, {"C:/absolute.txt", "hello"}, {"a\\b.txt", "hello"},
		{"a/../b.txt", "hello"}, {"a//b.txt", "hello"},
	} {
		t.Run(tc.name+tc.content[:min(len(tc.content), 6)], func(t *testing.T) {
			if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload(tc.name, tc.content)}); err == nil {
				t.Fatal("accepted invalid import")
			}
		})
	}
	if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{{Name: "bad.txt", ContentBase64: "!invalid!"}}); err == nil {
		t.Fatal("accepted malformed base64")
	}
	if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("too-big.txt", strings.Repeat("x", (16<<20)+1))}); err == nil {
		t.Fatal("accepted oversized import")
	}
	state, err := ReadData(root, id, "")
	if err != nil || len(state.Datasets) != 1 || state.Dataset.ID != good.ID {
		t.Fatalf("failed imports damaged prior state: %+v, %v", state, err)
	}
}

func TestImportDataConfinesStorageAndRejectsSymlinks(t *testing.T) {
	for _, component := range []string{"data", "data/datasets", "data/selection.json"} {
		t.Run(component, func(t *testing.T) {
			root, id := dataFixture(t, "custom")
			outside := t.TempDir()
			path := filepath.Join(root, "models", id, filepath.FromSlash(component))
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("ok.txt", "hello\n")}); component != "data/selection.json" && err == nil {
				t.Fatal("import followed symlink")
			}
			if _, err := ReadData(root, id, ""); err == nil {
				t.Fatal("read followed symlink")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote outside project")
			}
		})
	}
	root, id := dataFixture(t, "custom")
	if _, err := ImportData(root, "../"+id, DataSource{Kind: "local"}, []DataFile{upload("ok.txt", "hello")}); err == nil {
		t.Fatal("accepted model traversal")
	}
	if _, err := ReadData(root, id, "../../escape"); err == nil {
		t.Fatal("accepted dataset traversal")
	}
}

type preparedFixtureRow struct {
	Row           int       `json:"row"`
	Inputs        []float64 `json:"inputs"`
	Target        float64   `json:"target"`
	InputIDs      []int     `json:"inputIds"`
	Labels        []int     `json:"labels"`
	AttentionMask []int     `json:"attentionMask"`
	Image         string    `json:"image"`
}

func preparedRows(t *testing.T, root, id, version, split string) []preparedFixtureRow {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "models", id, "data", "prepared", version, split+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := []preparedFixtureRow{}
	for _, line := range bytes.Split(bytes.TrimSpace(content), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row preparedFixtureRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestPrepareDataUsesTrainingStatisticsAndDisjointReproducibleSplits(t *testing.T) {
	root, id := dataFixture(t, "classification")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("train.csv", "x,label\n1,dog\n2,cat\n3,dog\n4,cat\n5,dog\n6,cat\n7,dog\n8,cat\n9,dog\n1000,cat\n")})
	if err != nil {
		t.Fatal(err)
	}
	config := DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x", "label"}, TargetColumn: "label", Scale: "standard", TrainPercent: 60, ValidationPercent: 20, TestPercent: 20, Seed: 42}
	selection, err := PrepareData(root, id, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Config.InputColumns) != 1 || selection.Config.InputColumns[0] != "x" || !reflect.DeepEqual(selection.Classes, []string{"cat", "dog"}) || len(selection.Statistics) != 1 || selection.Statistics[0].Column != "x" {
		t.Fatalf("mapping = %+v", selection)
	}
	if selection.Counts != (DataCounts{Train: 6, Validation: 2, Test: 2}) {
		t.Fatalf("counts = %+v", selection.Counts)
	}
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 1000}
	seen := map[int]bool{}
	sum, sumsq := 0.0, 0.0
	train := preparedRows(t, root, id, selection.Version, "train")
	for _, row := range train {
		sum += values[row.Row]
		sumsq += values[row.Row] * values[row.Row]
	}
	mean := sum / 6
	std := math.Sqrt(sumsq/6 - mean*mean)
	if math.Abs(selection.Statistics[0].Mean-mean) > 1e-9 || math.Abs(selection.Statistics[0].Std-std) > 1e-9 {
		t.Fatalf("statistics included held-out rows: %+v; training mean=%g std=%g", selection.Statistics, mean, std)
	}
	for _, split := range []string{"train", "validation", "test"} {
		for _, row := range preparedRows(t, root, id, selection.Version, split) {
			if seen[row.Row] {
				t.Fatal("row leaked across splits")
			}
			seen[row.Row] = true
			if len(row.Inputs) != 1 || math.Abs(row.Inputs[0]-(values[row.Row]-mean)/std) > 1e-9 {
				t.Fatalf("transformation = %+v", row)
			}
			want := float64(1)
			if row.Row%2 == 1 {
				want = 0
			}
			if row.Target != want {
				t.Fatalf("wrong class encoding: %+v", row)
			}
		}
	}
	if len(seen) != 10 {
		t.Fatalf("split lost rows: %v", seen)
	}
	again, err := PrepareData(root, id, config)
	if err != nil || !reflect.DeepEqual(selection, again) {
		t.Fatalf("non-deterministic preparation = %+v, %v", again, err)
	}
	state, err := ReadData(root, id, "")
	if err != nil || state.Selection == nil || !reflect.DeepEqual(*state.Selection, selection) {
		t.Fatalf("saved preparation = %+v, %v", state, err)
	}
	config.Seed++
	different, err := PrepareData(root, id, config)
	if err != nil || different.Version == selection.Version {
		t.Fatalf("new seed did not create immutable version: %+v, %v", different, err)
	}
	if len(preparedRows(t, root, id, selection.Version, "train")) != 6 {
		t.Fatal("new preparation damaged old version")
	}
}

func TestPrepareDataRejectsInvalidAndPreservesSelection(t *testing.T) {
	root, id := dataFixture(t, "classification")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("valid.csv", "x,label\n1,cat\n2,dog\n")})
	if err != nil {
		t.Fatal(err)
	}
	valid := DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", TrainPercent: 100}
	saved, err := PrepareData(root, id, valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := []DataConfig{valid, valid, valid, valid, valid, valid, valid}
	cases[0].TrainPercent = 99
	cases[1].Purpose = "regression"
	cases[2].InputColumns = []string{"label"}
	cases[3].TargetColumn = "missing"
	cases[4].DatasetID = "../../other"
	cases[5].Mode = "image"
	cases[6].Scale = "magic"
	for i, config := range cases {
		if _, err := PrepareData(root, id, config); err == nil {
			t.Fatalf("accepted invalid config %d: %+v", i, config)
		}
	}
	state, err := ReadData(root, id, "")
	if err != nil || state.Selection == nil || !reflect.DeepEqual(*state.Selection, saved) {
		t.Fatalf("failed preparation changed selection: %+v, %v", state, err)
	}
}

func TestPrepareDataStratifiesClassesAndSupportsRegression(t *testing.T) {
	root, id := dataFixture(t, "custom")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("labels.csv", "x,label\n1,a\n2,a\n3,a\n4,a\n5,b\n6,b\n7,b\n8,b\n")})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "classification", InputColumns: []string{"x"}, TargetColumn: "label", TrainPercent: 50, ValidationPercent: 25, TestPercent: 25, Stratify: true, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, split := range []string{"train", "validation", "test"} {
		classes := map[float64]bool{}
		for _, row := range preparedRows(t, root, id, selection.Version, split) {
			classes[row.Target] = true
		}
		if len(classes) != 2 {
			t.Fatalf("%s did not preserve classes", split)
		}
	}
	ds, err = ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("regression.csv", "x,target\n1,2.5\n2,3.5\n")})
	if err != nil {
		t.Fatal(err)
	}
	selection, err = PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "features", Purpose: "regression", InputColumns: []string{"x"}, TargetColumn: "target", TrainPercent: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Classes) != 0 || selection.Config.Scale != "none" {
		t.Fatalf("regression metadata = %+v", selection)
	}
	for _, row := range preparedRows(t, root, id, selection.Version, "train") {
		if row.Target != []float64{2.5, 3.5}[row.Row] {
			t.Fatalf("regression target = %+v", row)
		}
	}
}

func TestPrepareDataUsesExplicitUTF8ByteTokenizerAndMasksPrompt(t *testing.T) {
	root, id := dataFixture(t, "text-generation")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("chat.jsonl", "{\"prompt\":\"A\",\"response\":\"é\"}\n")})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"prompt"}, TargetColumn: "response", SequenceLength: 8, TrainPercent: 100})
	if err != nil {
		t.Fatal(err)
	}
	rows := preparedRows(t, root, id, selection.Version, "train")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].InputIDs, []int{1, 68, 13, 198, 172, 2, 0, 0}) || !reflect.DeepEqual(rows[0].Labels, []int{-100, -100, -100, 198, 172, 2, -100, -100}) || !reflect.DeepEqual(rows[0].AttentionMask, []int{1, 1, 1, 1, 1, 1, 0, 0}) {
		t.Fatalf("byte tokens or prompt mask = %+v", rows)
	}
	content, err := os.ReadFile(filepath.Join(root, "models", id, "data", "prepared", selection.Version, "transform.json"))
	if err != nil {
		t.Fatal(err)
	}
	var transform struct {
		Tokenizer struct {
			Kind           string `json:"kind"`
			VocabularySize int    `json:"vocabularySize"`
		} `json:"tokenizer"`
	}
	if err := json.Unmarshal(content, &transform); err != nil {
		t.Fatal(err)
	}
	if transform.Tokenizer.Kind != "utf8-byte" || transform.Tokenizer.VocabularySize != 259 {
		t.Fatalf("misrepresented tokenizer: %s", content)
	}
	ds, err = ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("plain.txt", "hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"text"}, TrainPercent: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDataPersistsImageAssetsAndResizeRecipe(t *testing.T) {
	root, id := dataFixture(t, "classification")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{{Name: "cats/a.png", ContentBase64: base64.StdEncoding.EncodeToString(b.Bytes())}})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareData(root, id, DataConfig{DatasetID: ds.ID, Mode: "image", Purpose: "classification", InputColumns: []string{"image"}, TargetColumn: "label", TrainPercent: 100, ImageWidth: 16, ImageHeight: 12, ImageChannels: 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := preparedRows(t, root, id, selection.Version, "train")
	if len(rows) != 1 || rows[0].Image != "datasets/"+ds.ID+"/raw/cats/a.png" || len(rows[0].Inputs) != 0 || rows[0].Target != 0 {
		t.Fatalf("image assets were replaced with fake tensors: %+v", rows)
	}
	asset, err := os.ReadFile(filepath.Join(root, "models", id, "data", filepath.FromSlash(rows[0].Image)))
	if err != nil || !bytes.Equal(asset, b.Bytes()) {
		t.Fatalf("image asset lost: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "models", id, "data", "prepared", selection.Version, "transform.json"))
	if err != nil {
		t.Fatal(err)
	}
	var transform struct {
		Image struct {
			Width, Height, Channels int
			AssetPathBase           string `json:"assetPathBase"`
		} `json:"image"`
	}
	if err := json.Unmarshal(content, &transform); err != nil {
		t.Fatal(err)
	}
	if transform.Image.Width != 16 || transform.Image.Height != 12 || transform.Image.Channels != 1 || transform.Image.AssetPathBase != "model-data" {
		t.Fatalf("image recipe missing: %s", content)
	}
}

func TestPrepareDataBoundsPaddedOutputBeforeWritingAndValidatesSeed(t *testing.T) {
	root, id := dataFixture(t, "text-generation")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("small.txt", "hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	config := DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"text"}, TrainPercent: 100, SequenceLength: 8}
	saved, err := PrepareData(root, id, config)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []int64{-1, 9007199254740992} {
		config.Seed = seed
		if _, err := PrepareData(root, id, config); err == nil {
			t.Fatalf("accepted unsafe seed %d", seed)
		}
	}
	large, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("compact.txt", strings.Repeat("x\n", 6000))})
	if err != nil {
		t.Fatal(err)
	}
	config.DatasetID = large.ID
	config.Seed = 0
	config.SequenceLength = 8192
	if _, err := PrepareData(root, id, config); err == nil || !strings.Contains(err.Error(), "prepared") {
		t.Fatalf("unbounded padded output was accepted: %v", err)
	}
	state, err := ReadData(root, id, "")
	if err != nil || state.Selection == nil || state.Selection.Version != saved.Version {
		t.Fatalf("failed preparation changed saved pointer: %+v, %v", state, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "models", id, "data", "prepared"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed preparation left artifacts: %v, %v", entries, err)
	}
}

func TestImportDataRejectsTruncatedAndExcessivePixelImages(t *testing.T) {
	root, id := dataFixture(t, "custom")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	huge := append([]byte(nil), b.Bytes()...)
	binary.BigEndian.PutUint32(huge[16:20], 1<<20)
	binary.BigEndian.PutUint32(huge[20:24], 1<<20)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for _, content := range [][]byte{b.Bytes()[:len(b.Bytes())-12], huge} {
		if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{{Name: "bad.png", ContentBase64: base64.StdEncoding.EncodeToString(content)}}); err == nil {
			t.Fatal("accepted malformed or excessive-pixel image")
		}
	}
}

func TestDataLimitsAndMissingModel(t *testing.T) {
	root, id := dataFixture(t, "custom")
	if _, err := ReadData(root, "missing", ""); err == nil {
		t.Fatal("missing model was treated as an empty dataset")
	}
	files := make([]DataFile, 1001)
	for i := range files {
		files[i] = upload("same.txt", "hello")
	}
	if _, err := ImportData(root, id, DataSource{Kind: "local"}, files); err == nil {
		t.Fatal("accepted too many files")
	}
	if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("rows.txt", strings.Repeat("x\n", 100001))}); err == nil {
		t.Fatal("accepted too many rows")
	}
	if _, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("columns.csv", strings.Repeat("x,", 128)+"last\n"+strings.Repeat("1,", 128)+"1\n")}); err == nil {
		t.Fatal("accepted too many columns")
	}
}

func TestDataAtomicPublicationRejectsSourceSymlinks(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "selection.json"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outside.json"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside.json", filepath.Join(dir, ".pointer-replaced")); err != nil {
		t.Fatal(err)
	}
	if err := dataAtomicRename(root, ".pointer-replaced", "selection.json"); err == nil {
		t.Fatal("published a symlink over the previous pointer")
	}
	content, err := os.ReadFile(filepath.Join(dir, "selection.json"))
	if err != nil || string(content) != "saved" {
		t.Fatalf("old pointer changed: %s, %v", content, err)
	}
}

func TestDataPreparationFailureCleansStageAndPreservesSavedPointer(t *testing.T) {
	root, id := dataFixture(t, "text-generation")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("valid.txt", "hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	config := DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"text"}, TrainPercent: 100, SequenceLength: 8}
	saved, err := PrepareData(root, id, config)
	if err != nil {
		t.Fatal(err)
	}
	ds, err = ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("long.csv", "prompt,response\na very long prompt,answer\n")})
	if err != nil {
		t.Fatal(err)
	}
	config.DatasetID = ds.ID
	config.InputColumns = []string{"prompt"}
	config.TargetColumn = "response"
	if _, err := PrepareData(root, id, config); err == nil {
		t.Fatal("accepted a prompt that leaves no response token")
	}
	state, err := ReadData(root, id, "")
	if err != nil || state.Selection == nil || state.Selection.Version != saved.Version {
		t.Fatalf("failed transform changed pointer: %+v, %v", state, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "models", id, "data", "prepared"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed transform left partial prepared artifacts: %v, %v", entries, err)
	}
	outside := t.TempDir()
	other := saved.Config
	other.Seed = 2
	name := preparedDataVersion(other)
	if err := os.Symlink(outside, filepath.Join(root, "models", id, "data", "prepared", name)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareData(root, id, other); err == nil {
		t.Fatal("prepared through a version symlink")
	}
	state, err = ReadData(root, id, "")
	if err != nil || state.Selection == nil || state.Selection.Version != saved.Version {
		t.Fatalf("version symlink changed pointer: %+v, %v", state, err)
	}
	entries, err = os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote through prepared symlink: %v, %v", entries, err)
	}
}
