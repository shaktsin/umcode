package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func WriteReport(w io.Writer, r Report, format string) error {
	if format == "json" {
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(r)
	}
	if format != "markdown" {
		return errors.New("report format must be json or markdown")
	}
	if _, err := fmt.Fprintf(w, "# Evaluation report\n\nParent-program coverage: **%s**\n\nRegression: **%s**; live release: **%s**\n\n", r.ParentCoverage, r.Regression, r.LiveRelease); err != nil {
		return err
	}
	for _, v := range r.Limitations {
		if _, err := fmt.Fprintf(w, "- %s\n", v); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "\n| Model | Category | Comparison | Quality | n | Wins | Losses |\n|---|---|---|---|---:|---:|---:|\n"); err != nil {
		return err
	}
	for _, s := range r.Strata {
		if _, err := fmt.Fprintf(w, "| %s | %s | %s → %s | %s | %d | %d | %d |\n", s.Model, s.Category, s.Baseline, s.Treatment, s.Quality.Verdict, s.Quality.N, s.Quality.Wins, s.Quality.Losses); err != nil {
			return err
		}
	}
	for _, g := range r.Efficiency {
		if _, err := fmt.Fprintf(w, "\n- %s: %s (minimum reduction %.0f%%)\n", g.Name, g.Verdict, g.Threshold*100); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "\nComplete measurements and provenance:\n\n```json\n"); err != nil {
		return err
	}
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	if err := e.Encode(r); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, "```\n")
	return err
}
