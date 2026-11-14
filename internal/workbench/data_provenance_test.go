package workbench

import (
	"reflect"
	"testing"
)

func TestImportDataWithProvenanceRoundTrip(t *testing.T) {
	root, id := dataFixture(t, "custom")
	prov := &DataProvenance{CreatedBy: "assistant", Title: "t", Script: "print(1)", InputDatasetIDs: []string{"abc"}, StdoutSummary: "ok"}
	ds, err := ImportDataWithProvenance(root, id, DataSource{Kind: "assistant", Name: "made"}, []DataFile{upload("a.csv", "x,y\n1,2\n3,4\n5,6\n")}, prov)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Provenance == nil || !reflect.DeepEqual(*ds.Provenance, *prov) {
		t.Fatalf("imported provenance = %+v, want %+v", ds.Provenance, prov)
	}
	state, err := ReadData(root, id, ds.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dataset == nil || state.Dataset.Provenance == nil || !reflect.DeepEqual(*state.Dataset.Provenance, *prov) {
		t.Fatalf("read provenance = %+v, want %+v", state.Dataset, prov)
	}
}

func TestImportDataWithoutProvenanceUnchanged(t *testing.T) {
	root, id := dataFixture(t, "custom")
	ds, err := ImportData(root, id, DataSource{Kind: "local"}, []DataFile{upload("a.csv", "x,y\n1,2\n")})
	if err != nil {
		t.Fatal(err)
	}
	if ds.Provenance != nil {
		t.Fatalf("plain import provenance = %+v, want nil", ds.Provenance)
	}
	state, err := ReadData(root, id, ds.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dataset == nil || state.Dataset.Provenance != nil {
		t.Fatalf("read dataset = %+v, want provenance nil", state.Dataset)
	}
}
