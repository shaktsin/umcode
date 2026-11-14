package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDatasetsSharedColumnsAndSample(t *testing.T) {
	root, id := dataFixture(t, "custom")
	long := strings.Repeat("v", 300)
	a, err := ImportData(root, id, DataSource{Kind: "local", Name: "a"}, []DataFile{upload("a.csv", "text,label\n"+long+",pos\nshort,neg\n")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ImportData(root, id, DataSource{Kind: "local", Name: "b"}, []DataFile{upload("b.csv", "text,extra\nhello,1\n")})
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectDatasets(root, id, []string{a.ID, b.ID}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Datasets) != 2 {
		t.Fatalf("datasets = %d, want 2", len(report.Datasets))
	}
	if len(report.SharedColumns) != 1 || report.SharedColumns[0] != "text" {
		t.Fatalf("shared = %v, want [text]", report.SharedColumns)
	}
	first := report.Datasets[0]
	if first.Rows != 2 || len(first.Sample) != 2 {
		t.Fatalf("first dataset = %+v", first)
	}
	for _, row := range first.Sample {
		for _, cell := range row {
			if len(cell) > 120 {
				t.Fatalf("sample cell length %d exceeds 120", len(cell))
			}
		}
	}
	if first.TargetColumn != "label" || first.TargetCounts["pos"] != 1 || first.TargetCounts["neg"] != 1 {
		t.Fatalf("target = %q counts %v", first.TargetColumn, first.TargetCounts)
	}
}

func TestInspectDatasetsCapped(t *testing.T) {
	root, id := dataFixture(t, "custom")
	header := make([]string, 120)
	cells := make([]string, 120)
	for i := range header {
		header[i] = "column_with_a_rather_long_name_" + strings.Repeat("x", 20) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		cells[i] = strings.Repeat("y", 100)
	}
	rows := strings.Join(header, ",") + "\n"
	for range 50 {
		rows += strings.Join(cells, ",") + "\n"
	}
	ds, err := ImportData(root, id, DataSource{Kind: "local", Name: "wide"}, []DataFile{upload("wide.csv", rows)})
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectDatasets(root, id, []string{ds.ID}, 20)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 24576 {
		t.Fatalf("report is %d bytes, want <= 24576", len(encoded))
	}
	if !report.Truncated {
		t.Fatal("report should say it was truncated")
	}
}

func TestReadLocalDataFilesRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"../x.csv", "/etc/passwd", "a/../../x.csv"} {
		if _, err := ReadLocalDataFiles(dir, path); err == nil || !strings.Contains(err.Error(), "inside the project") {
			t.Fatalf("path %q error = %v, want inside-the-project rejection", path, err)
		}
	}
}

func TestReadLocalDataFilesReads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("x\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := ReadLocalDataFiles(dir, "a.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "a.csv" {
		t.Fatalf("files = %+v", files)
	}
}
