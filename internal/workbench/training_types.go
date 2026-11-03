package workbench

import (
	"encoding/json"
	"errors"
	"math"
)

type TrainingConfig struct {
	Epochs       int     `json:"epochs"`
	BatchSize    int     `json:"batchSize"`
	LearningRate float64 `json:"learningRate"`
	Seed         int64   `json:"seed"`
}

func (c TrainingConfig) Validate() error {
	if c.Epochs < 1 || c.Epochs > 1000 {
		return errors.New("epochs must be between 1 and 1,000")
	}
	if c.BatchSize < 1 || c.BatchSize > 256 {
		return errors.New("batch size must be between 1 and 256")
	}
	if math.IsNaN(c.LearningRate) || math.IsInf(c.LearningRate, 0) || c.LearningRate < .000001 || c.LearningRate > 1 {
		return errors.New("learning rate must be between 0.000001 and 1")
	}
	if c.Seed < 0 || c.Seed > 2147483647 {
		return errors.New("seed must be between 0 and 2,147,483,647")
	}
	return nil
}

type TrainingMetrics struct {
	Loss          float64  `json:"loss"`
	MAE           *float64 `json:"mae,omitempty"`
	RMSE          *float64 `json:"rmse,omitempty"`
	R2            *float64 `json:"r2,omitempty"`
	Accuracy      *float64 `json:"accuracy,omitempty"`
	MacroF1       *float64 `json:"macroF1,omitempty"`
	Perplexity    *float64 `json:"perplexity,omitempty"`
	TokenAccuracy *float64 `json:"tokenAccuracy,omitempty"`
	Samples       int      `json:"samples,omitempty"`
}
type TrainingEpoch struct {
	Epoch      int              `json:"epoch"`
	Train      *TrainingMetrics `json:"train"`
	Validation *TrainingMetrics `json:"validation"`
}
type TrainingRun struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	ModelCreatedAt     string           `json:"modelCreatedAt"`
	Status             string           `json:"status"`
	Config             TrainingConfig   `json:"config"`
	Epoch              int              `json:"epoch"`
	History            []TrainingEpoch  `json:"history"`
	Train              *TrainingMetrics `json:"train"`
	Validation         *TrainingMetrics `json:"validation"`
	Test               *TrainingMetrics `json:"test"`
	CheckpointID       string           `json:"checkpointId,omitempty"`
	Error              string           `json:"error,omitempty"`
	CreatedAt          string           `json:"createdAt"`
	FinishedAt         string           `json:"finishedAt,omitempty"`
	DurationMs         int64            `json:"durationMs"`
	PreparationVersion string           `json:"preparationVersion"`
	ParameterCount     int64            `json:"parameterCount,omitempty"`
}
type TrainingState struct {
	Runs             []TrainingRun `json:"runs"`
	RuntimeAvailable bool          `json:"runtimeAvailable"`
	Issues           []string      `json:"issues"`
}
type TrainingSnapshot struct {
	Graph     json.RawMessage
	Selection DataSelection
	Splits    map[string][]byte
}
