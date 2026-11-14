package workbench

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const maxInspectBytes = 24576

// InspectColumn summarizes one column for the assistant.
type InspectColumn struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Missing int    `json:"missing"`
}

// InspectDataset summarizes one imported dataset for the assistant.
type InspectDataset struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Rows         int                 `json:"rows"`
	Columns      []InspectColumn     `json:"columns"`
	Sample       []map[string]string `json:"sample"`
	TargetColumn string              `json:"targetColumn,omitempty"`
	TargetCounts map[string]int      `json:"targetCounts,omitempty"`
}

// InspectReport is the assistant-facing view across datasets.
type InspectReport struct {
	Datasets      []InspectDataset `json:"datasets"`
	SharedColumns []string         `json:"sharedColumns"`
	Truncated     bool             `json:"truncated,omitempty"`
}

func inspectCell(value string) string {
	count := 0
	for i := range value {
		if count == 120 {
			return value[:i]
		}
		count++
	}
	return value
}

func inspectTarget(ds Dataset, rows []dataRecord) (string, map[string]int) {
	target := ""
	for _, name := range []string{"label", "target", "class", "category"} {
		for _, column := range ds.Columns {
			if column.Name == name {
				target = name
			}
		}
	}
	if target == "" {
		for _, column := range ds.Columns {
			if column.Kind == "text" && column.UniqueCount >= 2 && column.UniqueCount <= 20 {
				target = column.Name
				break
			}
		}
	}
	if target == "" {
		return "", nil
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[inspectCell(row[target])]++
	}
	if len(counts) > 20 {
		values := make([]string, 0, len(counts))
		for value := range counts {
			values = append(values, value)
		}
		sort.Slice(values, func(i, j int) bool { return counts[values[i]] > counts[values[j]] })
		kept := map[string]int{}
		for _, value := range values[:20] {
			kept[value] = counts[value]
		}
		counts = kept
	}
	return target, counts
}

// InspectDatasets reads sample rows and summary statistics for the requested
// imported datasets (all of them when ids is empty), bounded to 24 KiB.
func InspectDatasets(projectRoot, modelID string, ids []string, sample int) (InspectReport, error) {
	sample = max(0, min(sample, 20))
	report := InspectReport{Datasets: []InspectDataset{}, SharedColumns: []string{}}
	state, err := ReadData(projectRoot, modelID, "")
	if err != nil {
		return report, err
	}
	byID := map[string]Dataset{}
	for _, ds := range state.Datasets {
		byID[ds.ID] = ds
	}
	ordered := state.Datasets
	if len(ids) > 0 {
		ordered = make([]Dataset, 0, len(ids))
		for _, id := range ids {
			ds, ok := byID[id]
			if !ok {
				return report, fmt.Errorf("dataset %s is not imported for this model", id)
			}
			ordered = append(ordered, ds)
		}
	}
	dataMu.Lock()
	defer dataMu.Unlock()
	root, _, err := openDataStorage(projectRoot, modelID, false)
	if err != nil {
		return report, err
	}
	defer root.Close()
	datasets, err := openDataDirectory(root, "datasets", false)
	if err != nil {
		return report, err
	}
	defer datasets.Close()
	columnDatasets := map[string]int{}
	for _, ds := range ordered {
		dsRoot, err := openDataDirectory(datasets, ds.ID, false)
		if err != nil {
			return report, err
		}
		rows, err := readDataRows(dsRoot, ds)
		dsRoot.Close()
		if err != nil {
			return report, err
		}
		entry := InspectDataset{ID: ds.ID, Name: ds.Name, Rows: ds.RowCount, Columns: []InspectColumn{}, Sample: []map[string]string{}}
		for _, column := range ds.Columns {
			entry.Columns = append(entry.Columns, InspectColumn{Name: column.Name, Kind: column.Kind, Missing: column.Missing})
			columnDatasets[column.Name]++
		}
		for _, row := range rows[:min(sample, len(rows))] {
			cells := map[string]string{}
			for _, column := range ds.Columns {
				cells[column.Name] = inspectCell(row[column.Name])
			}
			entry.Sample = append(entry.Sample, cells)
		}
		entry.TargetColumn, entry.TargetCounts = inspectTarget(ds, rows)
		report.Datasets = append(report.Datasets, entry)
	}
	if len(report.Datasets) > 1 {
		for name, count := range columnDatasets {
			if count == len(report.Datasets) {
				report.SharedColumns = append(report.SharedColumns, name)
			}
		}
		sort.Strings(report.SharedColumns)
	}
	for !fitsInspectBudget(report) {
		report.Truncated = true
		shrunk := false
		for i := range report.Datasets {
			entry := &report.Datasets[i]
			if len(entry.Sample) > 0 {
				entry.Sample = entry.Sample[:len(entry.Sample)-1]
				shrunk = true
			} else if len(entry.Columns) > 8 {
				entry.Columns = entry.Columns[:8]
				shrunk = true
			} else if entry.TargetCounts != nil {
				entry.TargetCounts = nil
				shrunk = true
			}
		}
		if !shrunk {
			break
		}
	}
	return report, nil
}

func fitsInspectBudget(report InspectReport) bool {
	encoded, err := json.Marshal(report)
	return err == nil && len(encoded) <= maxInspectBytes
}

// ReadLocalDataFiles reads one file from inside the project folder for an
// assistant-requested import. Absolute paths and any traversal outside the
// project are rejected before anything is opened.
func ReadLocalDataFiles(projectDir, relPath string) ([]DataFile, error) {
	cleaned := path.Clean(strings.ReplaceAll(relPath, "\\", "/"))
	if cleaned == "" || cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return nil, fmt.Errorf("path %q must stay inside the project folder", relPath)
	}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("file %q does not exist inside the project folder", relPath)
		}
		return nil, fmt.Errorf("open %q inside the project folder: %w", relPath, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxDataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxDataBytes {
		return nil, fmt.Errorf("file %q exceeds the %d MiB import limit", relPath, maxDataBytes/(1024*1024))
	}
	return []DataFile{{Name: path.Base(cleaned), ContentBase64: base64.StdEncoding.EncodeToString(content)}}, nil
}
