package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type Unit struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Cluster string `json:"cluster"`
	ArmID   string `json:"arm_id"`
	ModelID string `json:"model_id"`
	Repeat  int    `json:"repeat"`
	Primary bool   `json:"primary"`
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Schedule(m Manifest) ([]Unit, error) {
	if e := Validate(m); e != nil {
		return nil, e
	}
	var out []Unit
	seen := map[string]bool{}
	for _, model := range m.Models {
		for _, task := range m.Tasks {
			for repeat := 0; repeat < m.Repeats; repeat++ {
				for _, pair := range m.Comparisons {
					order := []string{pair.Baseline, pair.Treatment}
					key := fmt.Sprintf("%s/%s/%s/%d/%s/%s", m.Seed, model.ID, task.ID, repeat, pair.Baseline, pair.Treatment)
					h := sha256.Sum256([]byte(key))
					if h[0]&1 == 1 {
						order[0], order[1] = order[1], order[0]
					}
					for _, arm := range order {
						cluster := task.Cluster
						if cluster == "" {
							cluster = task.ID
						}
						id := Hash([]any{m.ID, model.ID, task.ID, repeat, arm})[:24]
						if seen[id] {
							continue
						}
						seen[id] = true
						out = append(out, Unit{id, task.ID, cluster, arm, model.ID, repeat, repeat == 0})
					}
				}
			}
		}
	}
	return out, nil
}
func (m Manifest) Task(id string) (Task, bool) {
	for _, t := range m.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
func (m Manifest) Arm(id string) (Arm, bool) {
	for _, a := range m.Arms {
		if a.ID == id {
			return a, true
		}
	}
	return Arm{}, false
}
func (m Manifest) Model(id string) (Model, bool) {
	for _, v := range m.Models {
		if v.ID == id {
			return v, true
		}
	}
	return Model{}, false
}
