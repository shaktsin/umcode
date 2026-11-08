package workbench

import (
	"errors"
	"os"
	"sort"
)

func ImportData(projectRoot, modelID string, source DataSource, files []DataFile) (Dataset, error) {
	dataMu.Lock()
	defer dataMu.Unlock()
	if _, err := Open(projectRoot, modelID); err != nil {
		return Dataset{}, err
	}
	decoded, id, byteCount, err := decodeDataFiles(files)
	if err != nil {
		return Dataset{}, err
	}
	source, err = normalizeDataSource(source, decoded)
	if err != nil {
		return Dataset{}, err
	}
	format, names, rows, err := parseData(decoded)
	if err != nil {
		return Dataset{}, err
	}
	ds := describeData(id, source, format, names, rows, byteCount)
	root, _, err := openDataStorage(projectRoot, modelID, true)
	if err != nil {
		return Dataset{}, err
	}
	defer root.Close()
	datasets, err := openDataDirectory(root, "datasets", true)
	if err != nil {
		return Dataset{}, err
	}
	defer datasets.Close()
	if _, err := datasets.Lstat(id); err == nil {
		return readDataset(datasets, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Dataset{}, err
	}
	name, stage, err := createDataStage(datasets, ".import-")
	if err != nil {
		return Dataset{}, err
	}
	defer stage.Close()
	published := false
	defer func() {
		if !published {
			discardDataStage(datasets, name, stage)
		}
	}()
	if err := writeDataRawFiles(stage, decoded); err != nil {
		return Dataset{}, err
	}
	if err := writeDataRows(stage, names, rows); err != nil {
		return Dataset{}, err
	}
	if err := writeNewDataJSON(stage, "metadata.json", ds); err != nil {
		return Dataset{}, err
	}
	if err := dataAtomicRename(datasets, name, id); err != nil {
		return Dataset{}, err
	}
	published = true
	return ds, nil
}

func ReadData(projectRoot, modelID, datasetID string) (DataState, error) {
	dataMu.Lock()
	defer dataMu.Unlock()
	state := DataState{Datasets: []Dataset{}}
	if datasetID != "" && !dataDigest.MatchString(datasetID) {
		return state, errors.New("invalid dataset id")
	}
	root, model, err := openDataStorage(projectRoot, modelID, false)
	if errors.Is(err, os.ErrNotExist) {
		if datasetID != "" || model.ID != modelID {
			return state, err
		}
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer root.Close()
	selection, err := readDataSelection(root)
	if err != nil {
		return state, err
	}
	state.Selection = selection
	datasets, err := openDataDirectory(root, "datasets", false)
	if errors.Is(err, os.ErrNotExist) && datasetID == "" && selection == nil {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer datasets.Close()
	f, err := datasets.Open(".")
	if err != nil {
		return state, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return state, err
	}
	for _, entry := range entries {
		if !dataDigest.MatchString(entry.Name()) {
			continue
		}
		ds, err := readDataset(datasets, entry.Name())
		if err != nil {
			return state, err
		}
		ds.Preview = nil
		state.Datasets = append(state.Datasets, ds)
	}
	sort.Slice(state.Datasets, func(i, j int) bool {
		if state.Datasets[i].CreatedAt == state.Datasets[j].CreatedAt {
			return state.Datasets[i].ID < state.Datasets[j].ID
		}
		return state.Datasets[i].CreatedAt > state.Datasets[j].CreatedAt
	})
	if datasetID == "" && selection != nil {
		datasetID = selection.DatasetID
	}
	if datasetID == "" && len(state.Datasets) > 0 {
		datasetID = state.Datasets[0].ID
	}
	if datasetID != "" {
		ds, err := readDataset(datasets, datasetID)
		if err != nil {
			return state, err
		}
		state.Dataset = &ds
	}
	return state, nil
}

func PrepareData(projectRoot, modelID string, config DataConfig) (DataSelection, error) {
	dataMu.Lock()
	defer dataMu.Unlock()
	return prepareData(projectRoot, modelID, config)
}

func readDataSelection(root *os.Root) (*DataSelection, error) {
	var pointer DataSelection
	if err := readDataJSON(root, "selection.json", maxPreparedDataBytes, &pointer); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if !dataDigest.MatchString(pointer.Version) || !dataDigest.MatchString(pointer.DatasetID) {
		return nil, errors.New("invalid saved preparation")
	}
	prepared, err := openDataDirectory(root, "prepared", false)
	if err != nil {
		return nil, err
	}
	defer prepared.Close()
	version, err := openDataDirectory(prepared, pointer.Version, false)
	if err != nil {
		return nil, err
	}
	defer version.Close()
	var selection DataSelection
	if err := readDataJSON(version, "metadata.json", maxPreparedDataBytes, &selection); err != nil {
		return nil, err
	}
	if selection.Version != pointer.Version || selection.DatasetID != pointer.DatasetID || selection.Config.DatasetID != selection.DatasetID {
		return nil, errors.New("saved preparation does not match its immutable version")
	}
	return &selection, nil
}
