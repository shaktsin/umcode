package workbench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxPreparedDataBytes int64 = 256 << 20

type dataOutputBudget struct{ remaining int64 }
type dataBudgetedWriter struct {
	destination io.Writer
	budget      *dataOutputBudget
}

func (writer dataBudgetedWriter) Write(content []byte) (int, error) {
	if int64(len(content)) > writer.budget.remaining {
		return 0, errors.New("prepared artifacts exceed the 256 MiB limit; use fewer rows or a shorter sequence length")
	}
	n, err := writer.destination.Write(content)
	writer.budget.remaining -= int64(n)
	return n, err
}

func validateDataOutputSize(rowCount int, config DataConfig) error {
	if config.Mode != "text" {
		return nil
	}
	// Every JSON token requires at least one digit plus a delimiter in each
	// input/mask/label array. Reject certain overflow before padding any rows.
	arrays := int64(2)
	if config.Purpose == "text-generation" {
		arrays = 3
	}
	if int64(rowCount)*int64(config.SequenceLength)*arrays*2 > maxPreparedDataBytes {
		return errors.New("prepared artifacts exceed the 256 MiB limit; use fewer rows or a shorter sequence length")
	}
	return nil
}

func writeBudgetedDataJSON(root *os.Root, name string, value any, budget *dataOutputBudget) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(dataBudgetedWriter{f, budget}).Encode(value); err != nil {
		return err
	}
	return f.Sync()
}

type dataTokenizerRecipe struct {
	Kind                    string `json:"kind"`
	VocabularySize          int    `json:"vocabularySize"`
	PadID                   int    `json:"padId"`
	BOSID                   int    `json:"bosId"`
	EOSID                   int    `json:"eosId"`
	ByteOffset              int    `json:"byteOffset"`
	SequenceLength          int    `json:"sequenceLength"`
	CausalShift             bool   `json:"causalShift"`
	PromptResponseSeparator string `json:"promptResponseSeparator"`
	IgnoreLabel             int    `json:"ignoreLabel"`
}

type dataImageRecipe struct {
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Channels      int    `json:"channels"`
	Resize        string `json:"resize"`
	AssetPathBase string `json:"assetPathBase"`
}

type dataTransform struct {
	SchemaVersion int                  `json:"schemaVersion"`
	DatasetID     string               `json:"datasetId"`
	Config        DataConfig           `json:"config"`
	Classes       []string             `json:"classes"`
	Statistics    []DataStatistic      `json:"statistics"`
	Tokenizer     *dataTokenizerRecipe `json:"tokenizer,omitempty"`
	Image         *dataImageRecipe     `json:"image,omitempty"`
}

func normalizeDataConfig(config DataConfig, ds Dataset, task string) (DataConfig, []string, error) {
	warnings := []string{}
	if config.Purpose != "classification" && config.Purpose != "regression" && config.Purpose != "text-generation" {
		return config, nil, errors.New("purpose must be classification, regression, or text-generation")
	}
	if task != "custom" && task != config.Purpose {
		return config, nil, fmt.Errorf("data purpose %s does not match model task %s", config.Purpose, task)
	}
	if config.Mode != "features" && config.Mode != "text" && config.Mode != "image" {
		return config, nil, errors.New("mode must be features, text, or image")
	}
	if config.Mode == "features" && ds.Format != "table" || config.Mode == "image" && ds.Format != "images" || config.Mode == "text" && ds.Format == "images" {
		return config, nil, errors.New("input mode does not match the dataset format")
	}
	if config.Purpose == "text-generation" && config.Mode != "text" {
		return config, nil, errors.New("text-generation requires text input mode")
	}
	if config.TargetColumn == "" && config.Purpose != "text-generation" {
		return config, nil, errors.New("choose a target column")
	}
	columns := map[string]DataColumn{}
	for _, column := range ds.Columns {
		columns[column.Name] = column
	}
	if config.TargetColumn != "" {
		if _, ok := columns[config.TargetColumn]; !ok {
			return config, nil, errors.New("target column does not exist")
		}
	}
	inputs := make([]string, 0, len(config.InputColumns))
	seen := map[string]bool{}
	for _, name := range config.InputColumns {
		if _, ok := columns[name]; !ok {
			return config, nil, fmt.Errorf("input column %q does not exist", name)
		}
		if name == config.TargetColumn {
			warnings = append(warnings, "The target column was removed from inputs.")
			continue
		}
		if seen[name] {
			return config, nil, errors.New("input columns must be unique")
		}
		seen[name] = true
		inputs = append(inputs, name)
		if config.Mode == "features" && columns[name].Kind != "number" {
			return config, nil, fmt.Errorf("feature column %q must be numeric", name)
		}
		if config.Mode == "image" && columns[name].Kind != "image" {
			return config, nil, errors.New("image input must use the image column")
		}
	}
	if len(inputs) == 0 {
		return config, nil, errors.New("choose at least one input column other than the target")
	}
	if config.Mode != "features" && len(inputs) != 1 {
		return config, nil, errors.New("text and image modes require exactly one input column")
	}
	config.InputColumns = inputs
	if config.Scale == "" {
		config.Scale = "none"
	}
	if config.Scale != "none" && config.Scale != "standard" {
		return config, nil, errors.New("scale must be none or standard")
	}
	if config.Mode != "features" {
		config.Scale = "none"
	}
	if config.TrainPercent == 0 && config.ValidationPercent == 0 && config.TestPercent == 0 {
		config.TrainPercent = 80
		config.ValidationPercent = 10
		config.TestPercent = 10
	}
	if config.TrainPercent < 1 || config.TrainPercent > 100 || config.ValidationPercent < 0 || config.ValidationPercent > 100 || config.TestPercent < 0 || config.TestPercent > 100 || config.TrainPercent+config.ValidationPercent+config.TestPercent != 100 {
		return config, nil, errors.New("split percentages must total 100 with at least 1% training data")
	}
	if config.Seed < 0 || config.Seed > 9007199254740991 {
		return config, nil, errors.New("seed must be an integer from 0 to 9,007,199,254,740,991")
	}
	if config.Purpose != "classification" {
		config.Stratify = false
	}
	if config.Mode == "text" {
		if config.SequenceLength == 0 {
			config.SequenceLength = 256
		}
		if config.SequenceLength < 3 || config.SequenceLength > 8192 {
			return config, nil, errors.New("text sequence length must be 3 to 8,192 bytes/tokens")
		}
	} else {
		config.SequenceLength = 0
	}
	if config.Mode == "image" {
		if config.ImageWidth == 0 {
			config.ImageWidth = 32
		}
		if config.ImageHeight == 0 {
			config.ImageHeight = 32
		}
		if config.ImageChannels == 0 {
			config.ImageChannels = 3
		}
		if config.ImageWidth < 1 || config.ImageWidth > 4096 || config.ImageHeight < 1 || config.ImageHeight > 4096 || config.ImageChannels != 1 && config.ImageChannels != 3 && config.ImageChannels != 4 {
			return config, nil, errors.New("image recipe requires dimensions 1 to 4,096 and 1, 3, or 4 channels")
		}
	} else {
		config.ImageWidth = 0
		config.ImageHeight = 0
		config.ImageChannels = 0
	}
	return config, warnings, nil
}

func dataFiniteNumber(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("missing numeric value")
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("expected a finite numeric value")
	}
	return v, nil
}

func validateDataMapping(rows []dataRecord, config DataConfig) ([]string, error) {
	classes := []string{}
	seen := map[string]bool{}
	for i, row := range rows {
		for _, input := range config.InputColumns {
			if config.Mode == "features" {
				if _, err := dataFiniteNumber(row[input]); err != nil {
					return nil, fmt.Errorf("row %d column %q: %w", i+1, input, err)
				}
			} else if strings.TrimSpace(row[input]) == "" {
				return nil, fmt.Errorf("row %d column %q is empty", i+1, input)
			}
		}
		if config.TargetColumn != "" {
			target := row[config.TargetColumn]
			if strings.TrimSpace(target) == "" {
				return nil, fmt.Errorf("row %d target is empty", i+1)
			}
			if config.Purpose == "regression" {
				if _, err := dataFiniteNumber(target); err != nil {
					return nil, fmt.Errorf("row %d regression target: %w", i+1, err)
				}
			}
			if config.Purpose == "classification" && !seen[target] {
				seen[target] = true
				classes = append(classes, target)
			}
		}
	}
	sort.Strings(classes)
	return classes, nil
}

func allocateDataSplit(n int, config DataConfig) [3]int {
	percents := [3]int{config.TrainPercent, config.ValidationPercent, config.TestPercent}
	counts := [3]int{}
	remainders := [3]int{}
	allocated := 0
	for i, p := range percents {
		counts[i] = n * p / 100
		remainders[i] = n * p % 100
		allocated += counts[i]
	}
	for allocated < n {
		best := 0
		for i := 1; i < 3; i++ {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		counts[best]++
		remainders[best] = -1
		allocated++
	}
	if n > 0 && counts[0] == 0 {
		donor := 1
		if counts[2] > counts[1] {
			donor = 2
		}
		counts[donor]--
		counts[0]++
	}
	return counts
}

func splitDataRows(rows []dataRecord, config DataConfig, classes []string) ([3][]int, []string) {
	splits := [3][]int{[]int{}, []int{}, []int{}}
	warnings := []string{}
	random := rand.New(rand.NewSource(config.Seed))
	groups := [][]int{}
	if config.Stratify {
		byClass := map[string][]int{}
		for i, row := range rows {
			byClass[row[config.TargetColumn]] = append(byClass[row[config.TargetColumn]], i)
		}
		for _, class := range classes {
			groups = append(groups, byClass[class])
			if len(byClass[class]) < 3 && config.ValidationPercent > 0 && config.TestPercent > 0 {
				warnings = append(warnings, fmt.Sprintf("Class %q has too few rows to appear in every split.", dataCellPreview(class)))
			}
		}
	} else {
		group := make([]int, len(rows))
		for i := range group {
			group[i] = i
		}
		groups = append(groups, group)
	}
	for _, group := range groups {
		random.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
		counts := allocateDataSplit(len(group), config)
		start := 0
		for split, count := range counts {
			splits[split] = append(splits[split], group[start:start+count]...)
			start += count
		}
	}
	for index, percent := range []int{config.TrainPercent, config.ValidationPercent, config.TestPercent} {
		if percent > 0 && len(splits[index]) == 0 {
			name := []string{"training", "validation", "test"}[index]
			warnings = append(warnings, fmt.Sprintf("The requested %s split has no rows after rounding. Use more rows, change split percentages, or turn off stratification.", name))
		}
	}
	return splits, warnings
}

func fitDataStatistics(rows []dataRecord, train []int, config DataConfig) ([]DataStatistic, []string, error) {
	stats := []DataStatistic{}
	warnings := []string{}
	if config.Mode != "features" || config.Scale != "standard" {
		return stats, warnings, nil
	}
	for _, column := range config.InputColumns {
		mean := 0.0
		for i, index := range train {
			v, _ := dataFiniteNumber(rows[index][column])
			n := float64(i + 1)
			mean = mean*((n-1)/n) + v/n
		}
		std := 0.0
		sqrtN := math.Sqrt(float64(len(train)))
		for _, index := range train {
			v, _ := dataFiniteNumber(rows[index][column])
			std = math.Hypot(std, v/sqrtN-mean/sqrtN)
		}
		if math.IsInf(mean, 0) || math.IsNaN(mean) || math.IsInf(std, 0) || math.IsNaN(std) {
			return nil, nil, fmt.Errorf("column %q has an unrepresentable training mean or standard deviation", column)
		}
		if std == 0 {
			warnings = append(warnings, fmt.Sprintf("Column %q is constant in the training split; scaling uses a divisor of 1.", column))
		}
		stats = append(stats, DataStatistic{Column: column, Mean: mean, Std: std})
	}
	return stats, warnings, nil
}

func preparedDataVersion(config DataConfig) string {
	content, _ := json.Marshal(struct {
		Schema int        `json:"schema"`
		Config DataConfig `json:"config"`
	}{1, config})
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func makeDataTransform(config DataConfig, classes []string, stats []DataStatistic) dataTransform {
	transform := dataTransform{SchemaVersion: 1, DatasetID: config.DatasetID, Config: config, Classes: classes, Statistics: stats}
	if config.Mode == "text" {
		transform.Tokenizer = &dataTokenizerRecipe{Kind: "utf8-byte", VocabularySize: 259, PadID: 0, BOSID: 1, EOSID: 2, ByteOffset: 3, SequenceLength: config.SequenceLength, CausalShift: config.Purpose == "text-generation", PromptResponseSeparator: "\n", IgnoreLabel: -100}
	}
	if config.Mode == "image" {
		transform.Image = &dataImageRecipe{Width: config.ImageWidth, Height: config.ImageHeight, Channels: config.ImageChannels, Resize: "bilinear", AssetPathBase: "model-data"}
	}
	return transform
}

func transformDataRow(index int, row dataRecord, transform dataTransform) (map[string]any, bool, error) {
	config := transform.Config
	record := map[string]any{"row": index}
	truncated := false
	if config.Purpose == "classification" {
		record["target"] = sort.SearchStrings(transform.Classes, row[config.TargetColumn])
	}
	if config.Purpose == "regression" {
		target, _ := dataFiniteNumber(row[config.TargetColumn])
		record["target"] = target
	}
	switch config.Mode {
	case "features":
		inputs := make([]float64, len(config.InputColumns))
		for i, column := range config.InputColumns {
			value, _ := dataFiniteNumber(row[column])
			if config.Scale == "standard" {
				stat := transform.Statistics[i]
				divisor := stat.Std
				if divisor == 0 {
					divisor = 1
				}
				difference := value - stat.Mean
				if math.IsInf(difference, 0) {
					value = value/divisor - stat.Mean/divisor
				} else {
					value = difference / divisor
				}
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, false, fmt.Errorf("row %d column %q overflows after scaling", index+1, column)
			}
			inputs[i] = value
		}
		record["inputs"] = inputs
	case "image":
		record["image"] = "datasets/" + config.DatasetID + "/raw/" + row[config.InputColumns[0]]
	case "text":
		input := []byte(row[config.InputColumns[0]])
		promptLength := 0
		if config.Purpose == "text-generation" && config.TargetColumn != "" {
			input = append(append(input, '\n'), []byte(row[config.TargetColumn])...)
			promptLength = len([]byte(row[config.InputColumns[0]])) + 1
			if promptLength >= config.SequenceLength-2 {
				return nil, false, fmt.Errorf("row %d prompt leaves no response tokens; increase sequence length", index+1)
			}
		}
		if len(input) > config.SequenceLength-2 {
			input = input[:config.SequenceLength-2]
			truncated = true
		}
		ids := make([]int, config.SequenceLength)
		mask := make([]int, config.SequenceLength)
		ids[0] = 1
		mask[0] = 1
		for i, value := range input {
			ids[i+1] = int(value) + 3
			mask[i+1] = 1
		}
		ids[len(input)+1] = 2
		mask[len(input)+1] = 1
		record["inputIds"] = ids
		record["attentionMask"] = mask
		if config.Purpose == "text-generation" {
			labels := make([]int, len(ids))
			for i, value := range ids {
				labels[i] = value
				if mask[i] == 0 || i == 0 || i <= promptLength {
					labels[i] = -100
				}
			}
			record["labels"] = labels
		}
	}
	return record, truncated, nil
}

func writePreparedDataSplit(root *os.Root, name string, indices []int, rows []dataRecord, transform dataTransform, budget *dataOutputBudget) (bool, error) {
	f, err := root.OpenFile(name+".jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	enc := json.NewEncoder(dataBudgetedWriter{f, budget})
	truncated := false
	for _, index := range indices {
		record, cut, err := transformDataRow(index, rows[index], transform)
		if err != nil {
			return false, err
		}
		truncated = truncated || cut
		if err := enc.Encode(record); err != nil {
			return false, err
		}
	}
	return truncated, f.Sync()
}

func verifyDataImageAssets(root *os.Root, rows []dataRecord, column string) error {
	raw, err := openDataDirectory(root, "raw", false)
	if err != nil {
		return err
	}
	defer raw.Close()
	for _, row := range rows {
		name := row[column]
		if !safeDataPath(name) {
			return errors.New("invalid stored image path")
		}
		parts := strings.Split(name, "/")
		parent := raw
		opened := []*os.Root{}
		for _, part := range parts[:len(parts)-1] {
			next, e := openDataDirectory(parent, part, false)
			if e != nil {
				for _, dir := range opened {
					dir.Close()
				}
				return e
			}
			opened = append(opened, next)
			parent = next
		}
		f, e := openDataRegularFile(parent, parts[len(parts)-1])
		if e == nil {
			f.Close()
		}
		for _, dir := range opened {
			dir.Close()
		}
		if e != nil {
			return e
		}
	}
	return nil
}

func prepareData(projectRoot, modelID string, config DataConfig) (DataSelection, error) {
	if !dataDigest.MatchString(config.DatasetID) {
		return DataSelection{}, errors.New("invalid dataset id")
	}
	root, model, err := openDataStorage(projectRoot, modelID, false)
	if err != nil {
		return DataSelection{}, err
	}
	defer root.Close()
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
	if err := validateDataOutputSize(ds.RowCount, config); err != nil {
		return DataSelection{}, err
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
	classes, err := validateDataMapping(rows, config)
	if err != nil {
		return DataSelection{}, err
	}
	if config.Mode == "image" {
		if err := verifyDataImageAssets(dataset, rows, config.InputColumns[0]); err != nil {
			return DataSelection{}, err
		}
	}
	splits, splitWarnings := splitDataRows(rows, config, classes)
	warnings = append(warnings, splitWarnings...)
	stats, statWarnings, err := fitDataStatistics(rows, splits[0], config)
	if err != nil {
		return DataSelection{}, err
	}
	warnings = append(warnings, statWarnings...)
	if config.Purpose == "classification" && len(classes) == 1 {
		warnings = append(warnings, "The dataset contains only one target class.")
	}
	selection := DataSelection{Version: preparedDataVersion(config), DatasetID: ds.ID, Config: config, Counts: DataCounts{Train: len(splits[0]), Validation: len(splits[1]), Test: len(splits[2])}, Classes: classes, Statistics: stats, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Warnings: warnings}
	prepared, err := openDataDirectory(root, "prepared", true)
	if err != nil {
		return DataSelection{}, err
	}
	defer prepared.Close()
	if _, err := prepared.Lstat(selection.Version); err == nil {
		version, e := openDataDirectory(prepared, selection.Version, false)
		if e != nil {
			return DataSelection{}, e
		}
		defer version.Close()
		if e := readDataJSON(version, "metadata.json", maxPreparedDataBytes, &selection); e != nil {
			return DataSelection{}, e
		}
		if selection.Version != preparedDataVersion(config) || selection.DatasetID != ds.ID {
			return DataSelection{}, errors.New("invalid existing prepared version")
		}
		if e := atomicDataJSON(root, "selection.json", selection); e != nil {
			return DataSelection{}, e
		}
		return selection, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return DataSelection{}, err
	}
	name, stage, err := createDataStage(prepared, ".prepare-")
	if err != nil {
		return DataSelection{}, err
	}
	defer stage.Close()
	published := false
	defer func() {
		if !published {
			discardDataStage(prepared, name, stage)
		}
	}()
	transform := makeDataTransform(config, classes, stats)
	truncated := false
	budget := &dataOutputBudget{remaining: maxPreparedDataBytes}
	for i, split := range []string{"train", "validation", "test"} {
		cut, e := writePreparedDataSplit(stage, split, splits[i], rows, transform, budget)
		if e != nil {
			return DataSelection{}, e
		}
		truncated = truncated || cut
	}
	if truncated {
		selection.Warnings = append(selection.Warnings, "Some text rows were truncated to the configured UTF-8 byte sequence length.")
	}
	if err := writeBudgetedDataJSON(stage, "transform.json", transform, budget); err != nil {
		return DataSelection{}, err
	}
	if err := writeBudgetedDataJSON(stage, "metadata.json", selection, budget); err != nil {
		return DataSelection{}, err
	}
	if err := dataAtomicRename(prepared, name, selection.Version); err != nil {
		return DataSelection{}, err
	}
	published = true
	if err := atomicDataJSON(root, "selection.json", selection); err != nil {
		return DataSelection{}, err
	}
	return selection, nil
}
