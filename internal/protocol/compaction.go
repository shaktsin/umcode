package protocol

import "time"

const NotifyCompactionUpdated = "thread/compactionUpdated"

type CompactionState struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	Sequence  int64     `json:"sequence"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Error     string    `json:"error,omitempty"`
}

func (s CompactionState) Terminal() bool {
	switch s.Status {
	case "completed", "failed", "cancelled", "interrupted":
		return true
	}
	return false
}
