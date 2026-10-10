package engine

// requiredToolFamilies uses engine-owned session state, never transcript prose.
func (e *Engine) requiredToolFamilies(threadID string) []string {
	if threadID == "" {
		return nil
	}
	var families []string
	if e.Exec != nil && e.Exec.ActiveForThread(threadID) {
		families = append(families, "core")
	}
	browser := e.VisualQA != nil && e.VisualQA.ForThread(threadID) != nil
	if e.Previews != nil {
		for _, p := range e.Previews.List() {
			if p.ThreadID == threadID {
				browser = true
				break
			}
		}
	}
	if browser {
		families = append(families, "browser")
	}
	if e.ComputerUse != nil && e.ComputerUse.ForThread(threadID) != nil {
		families = append(families, "computer")
	}
	return families
}
