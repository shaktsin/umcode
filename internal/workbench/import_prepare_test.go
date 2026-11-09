package workbench

import "testing"

func TestImportedPreparationRejectsInvalidDataset(t *testing.T) {
	root, id, _, _, _ := runtimeBundleFixture(t)
	if _, err := PrepareImportedData(root, id, DataConfig{DatasetID: "../escape"}); err == nil {
		t.Fatal("accepted invalid dataset identity")
	}
}

func TestImportedPreparationPreservesRawText(t *testing.T) {
	root, id, created, _, _ := runtimeBundleFixture(t)
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("text.csv", "text\nhello\nworld\nthird\nfourth\nfifth\n")})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareImportedData(root, id, DataConfig{DatasetID: ds.ID, Mode: "text", Purpose: "text-generation", InputColumns: []string{"text"}, TrainPercent: 60, ValidationPercent: 20, TestPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	splits, saved, err := readImportedSplits(BundleTarget{root, id, created})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != selection.Version || len(splits.Train) != 3 || len(splits.Validation) != 1 || len(splits.Test) != 1 {
		t.Fatalf("incorrect snapshot: %+v %+v", saved, splits)
	}
	seen := map[string]bool{}
	for _, part := range [][]importedRow{splits.Train, splits.Validation, splits.Test} {
		for _, row := range part {
			seen[row.Text] = true
		}
	}
	for _, text := range []string{"hello", "world", "third", "fourth", "fifth"} {
		if !seen[text] {
			t.Fatalf("lost text %q", text)
		}
	}
}
