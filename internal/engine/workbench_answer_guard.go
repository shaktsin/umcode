package engine

import (
	"context"
	"regexp"

	"github.com/shaktsin/umcode/internal/workbench"
)

var workbenchRunID = regexp.MustCompile(`\btrain-[a-f0-9]{32}\b`)
var claimedTrainingStart = regexp.MustCompile(`(?i)\b(?:i|we)\s+(?:have\s+|just\s+)?(?:started|launched|kicked off)\b|\bstarted (?:for real|training|a new training run)\b`)

var claimedDatasetCreation = regexp.MustCompile(`(?i)\b(?:i|we)(?:['’]ve|\s+have|\s+just)?\s+(?:created|made|generated|built)\s+(?:(?:a|an|the|new|your|balanced|training|prepared|cleaned|combined|merged)\s+){0,3}dataset\b`)

const unverifiedDatasetAnswer = "I could not verify a new dataset. No script run is confirmed by a Workbench tool result. I need to run an approved data script and report the dataset it actually produced before saying one was created."

const unverifiedTrainingAnswer = "I could not verify a new training run. No start is confirmed by a Workbench tool result. I need to call the Train action and report its actual run ID before saying training started."

// A Workbench answer cannot turn an invented run ID or a first-person claim
// into evidence of execution. Tool results and saved run records are authority.
func (e *Engine) guardWorkbenchAnswer(ctx context.Context, answer string) string {
	c := workbenchContextFrom(ctx)
	if c == nil || answer == "" {
		return answer
	}
	if claimedDatasetCreation.MatchString(answer) && len(c.CreatedDatasetIDs) == 0 {
		return unverifiedDatasetAnswer
	}
	if claimedTrainingStart.MatchString(answer) && len(c.StartedRunIDs) == 0 {
		return unverifiedTrainingAnswer
	}
	ids := workbenchRunID.FindAllString(answer, -1)
	if len(ids) == 0 {
		return answer
	}
	known := map[string]bool{}
	for _, id := range c.StartedRunIDs {
		known[id] = true
	}
	allCurrent := true
	for _, id := range ids {
		if !known[id] {
			allCurrent = false
			break
		}
	}
	if allCurrent {
		return answer
	}
	project, err := e.project(ctx, c.ProjectID)
	if err == nil {
		runs, readErr := workbench.ReadTrainingRuns(project.Root, c.ModelID, c.ModelCreatedAt)
		if readErr == nil {
			for _, run := range runs {
				known[run.ID] = true
			}
		}
	}
	for _, id := range ids {
		if !known[id] {
			return unverifiedTrainingAnswer
		}
	}
	return answer
}
