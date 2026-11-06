package workbench

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type importedRow struct {
	Text        string `json:"text,omitempty"`
	ImageBase64 string `json:"imageBase64,omitempty"`
	Target      *int   `json:"target,omitempty"`
}
type importedSplits struct {
	Train      []importedRow `json:"train"`
	Validation []importedRow `json:"validation"`
	Test       []importedRow `json:"test"`
}

func PrepareImportedData(projectRoot, modelID string, config DataConfig) (DataSelection, error) {
	dataMu.Lock()
	defer dataMu.Unlock()
	if !dataDigest.MatchString(config.DatasetID) {
		return DataSelection{}, errors.New("invalid dataset id")
	}
	root, model, err := openDataStorage(projectRoot, modelID, false)
	if err != nil {
		return DataSelection{}, err
	}
	defer root.Close()
	b := BundleTarget{projectRoot, modelID, model.CreatedAt.Format(time.RFC3339Nano)}
	mr, err := b.model()
	if err != nil {
		return DataSelection{}, err
	}
	raw, err := readDataFile(mr, "graph.json", maxImportedGraphBytes)
	mr.Close()
	if err != nil {
		return DataSelection{}, err
	}
	g, err := RuntimeArchitecture(raw)
	if err != nil {
		return DataSelection{}, err
	}
	task, err := importedTask(g)
	if err != nil {
		return DataSelection{}, err
	}
	datasets, err := openDataDirectory(root, "datasets", false)
	if err != nil {
		return DataSelection{}, err
	}
	defer datasets.Close()
	ds, err := readDataset(datasets, config.DatasetID)
	if err != nil {
		return DataSelection{}, err
	}
	config, warnings, err := normalizeDataConfig(config, ds, model.Task)
	if err != nil {
		return DataSelection{}, err
	}
	if (task == "image-classification" && config.Mode != "image") || (task != "image-classification" && config.Mode != "text") {
		return DataSelection{}, errors.New("Dataset input mode does not match the imported model task.")
	}
	if config.SequenceLength > 2048 || config.Seed > 4294967295 {
		return DataSelection{}, errors.New("Imported runtime supports sequence lengths up to 2,048 and seeds up to 4,294,967,295.")
	}
	dataset, err := openDataDirectory(datasets, ds.ID, false)
	if err != nil {
		return DataSelection{}, err
	}
	defer dataset.Close()
	rows, err := readDataRows(dataset, ds)
	if err != nil {
		return DataSelection{}, err
	}
	if len(rows) > 1000 {
		return DataSelection{}, errors.New("Imported training supports at most 1,000 rows.")
	}
	classes, err := validateDataMapping(rows, config)
	if err != nil {
		return DataSelection{}, err
	}
	if config.Mode == "image" {
		if err = verifyDataImageAssets(dataset, rows, config.InputColumns[0]); err != nil {
			return DataSelection{}, err
		}
	}
	indices, sw := splitDataRows(rows, config, classes)
	warnings = append(warnings, sw...)
	labels := map[string]int{}
	count := 0
	if task != "text-generation" {
		modelRoot, e := b.model()
		if e != nil {
			return DataSelection{}, e
		}
		var receipt importedReceipt
		e = readDataJSON(modelRoot, "import-runtime.json", 1<<20, &receipt)
		modelRoot.Close()
		if e != nil || !receipt.State.Ready || len(receipt.State.Labels) == 0 {
			return DataSelection{}, errors.New("Validate the model to discover its classification labels before preparing data.")
		}
		for i, label := range receipt.State.Labels {
			if _, duplicate := labels[label]; duplicate {
				return DataSelection{}, errors.New("The pretrained model has ambiguous duplicate labels.")
			}
			labels[label] = i
		}
		count = len(receipt.State.Labels)
	}
	convert := func(index int) (importedRow, error) {
		row := rows[index]
		out := importedRow{}
		input := row[config.InputColumns[0]]
		if config.Mode == "text" {
			out.Text = input
			if config.Purpose == "text-generation" && config.TargetColumn != "" {
				out.Text += "\n" + row[config.TargetColumn]
			}
		} else {
			rawdir, e := openDataDirectory(dataset, "raw", false)
			if e != nil {
				return out, e
			}
			content, e := readDataFile(rawdir, input, 16<<20)
			rawdir.Close()
			if e != nil {
				return out, e
			}
			out.ImageBase64 = base64.StdEncoding.EncodeToString(content)
		}
		if config.Purpose == "classification" {
			value := row[config.TargetColumn]
			idx, e := strconv.Atoi(value)
			if e != nil {
				var ok bool
				idx, ok = labels[value]
				if !ok {
					return out, fmt.Errorf("Target %q is not an index or an exact pretrained label.", dataCellPreview(value))
				}
			}
			if idx < 0 || idx >= count {
				return out, errors.New("Target index is outside the pretrained classification head.")
			}
			out.Target = &idx
		}
		return out, nil
	}
	all := make([][]importedRow, 3)
	for i, part := range indices {
		all[i] = make([]importedRow, 0, len(part))
		for _, index := range part {
			v, e := convert(index)
			if e != nil {
				return DataSelection{}, e
			}
			all[i] = append(all[i], v)
		}
	}
	splits := importedSplits{all[0], all[1], all[2]}
	encoded, err := json.Marshal(splits)
	if err != nil {
		return DataSelection{}, err
	}
	if len(encoded) > int(maxPreparedDataBytes) {
		return DataSelection{}, errors.New("Prepared data exceeds size limit.")
	}
	versionBytes, _ := json.Marshal(struct {
		Schema string
		Config DataConfig
		Source ImportedSource
	}{"imported-v1", config, g.Source})
	hash := sha256.Sum256(versionBytes)
	version := hex.EncodeToString(hash[:])
	selection := DataSelection{Version: version, DatasetID: ds.ID, Config: config, Counts: DataCounts{Train: len(all[0]), Validation: len(all[1]), Test: len(all[2])}, Classes: classes, Statistics: []DataStatistic{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Warnings: warnings}
	prepared, err := openDataDirectory(root, "prepared", true)
	if err != nil {
		return DataSelection{}, err
	}
	defer prepared.Close()
	if _, err = prepared.Lstat(version); errors.Is(err, os.ErrNotExist) {
		name, stage, e := createDataStage(prepared, ".prepare-")
		if e != nil {
			return DataSelection{}, e
		}
		defer stage.Close()
		published := false
		defer func() {
			if !published {
				discardDataStage(prepared, name, stage)
			}
		}()
		if e = writeNewDataJSON(stage, "imported.json", splits); e != nil {
			return DataSelection{}, e
		}
		if e = writeNewDataJSON(stage, "metadata.json", selection); e != nil {
			return DataSelection{}, e
		}
		if e = dataAtomicRename(prepared, name, version); e != nil {
			return DataSelection{}, e
		}
		published = true
	} else if err != nil {
		return DataSelection{}, err
	} else {
		existing, e := openDataDirectory(prepared, version, false)
		if e != nil {
			return DataSelection{}, e
		}
		defer existing.Close()
		if e = readDataJSON(existing, "metadata.json", maxPreparedDataBytes, &selection); e != nil {
			return DataSelection{}, e
		}
	}
	if err = atomicDataJSON(root, "selection.json", selection); err != nil {
		return DataSelection{}, err
	}
	return selection, nil
}
func readImportedSplits(b BundleTarget) (importedSplits, DataSelection, error) {
	dataMu.Lock()
	defer dataMu.Unlock()
	var splits importedSplits
	model, err := b.model()
	if err != nil {
		return splits, DataSelection{}, err
	}
	model.Close()
	root, _, err := openDataStorage(b.ProjectRoot, b.ModelID, false)
	if err != nil {
		return splits, DataSelection{}, err
	}
	defer root.Close()
	selection, err := readDataSelection(root)
	if err != nil {
		return splits, DataSelection{}, err
	}
	if selection == nil {
		return splits, DataSelection{}, errors.New("Prepare data before training.")
	}
	prepared, err := openDataDirectory(root, "prepared", false)
	if err != nil {
		return splits, DataSelection{}, err
	}
	defer prepared.Close()
	version, err := openDataDirectory(prepared, selection.Version, false)
	if err != nil {
		return splits, DataSelection{}, err
	}
	defer version.Close()
	if err = readDataJSON(version, "imported.json", maxPreparedDataBytes, &splits); err != nil {
		return splits, DataSelection{}, errors.New("Prepare this dataset for the imported runtime.")
	}
	return splits, *selection, nil
}
