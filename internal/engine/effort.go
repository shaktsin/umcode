package engine

// lowerLevel returns the effort level one step below level; off stays off and
// an unrecognized value is returned unchanged.
func lowerLevel(level string) string {
	switch level {
	case "high":
		return "medium"
	case "medium":
		return "low"
	case "low", "off":
		return "off"
	}
	return level
}
