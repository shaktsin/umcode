// umcode-eval runs isolated, explicitly configured evaluation campaigns.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/evaluation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func loadManifest(path string) (evaluation.Manifest, error) {
	var m evaluation.Manifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	m, err = evaluation.DecodeManifest(f)
	if err != nil {
		return m, err
	}
	m.BaseDir, err = filepath.Abs(filepath.Dir(path))
	return m, err
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	bad := func(err error) int { fmt.Fprintln(stderr, err); return 2 }
	if len(args) == 0 {
		return bad(errors.New("usage: umcode-eval validate|run|review-export|review-import|report|cleanup"))
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var manifest, out, destination, scores, format, revision, campaign string
	var live, resume, requirePass bool
	var ceiling float64
	switch args[0] {
	case "validate":
		fs.StringVar(&manifest, "manifest", "", "manifest JSON")
	case "run":
		fs.StringVar(&manifest, "manifest", "", "manifest JSON")
		fs.StringVar(&out, "out", "", "new owned campaign directory")
		fs.BoolVar(&live, "live", false, "authorize configured paid API calls")
		fs.Float64Var(&ceiling, "max-cost-usd", 0, "live spending ceiling")
		fs.BoolVar(&resume, "resume", false, "resume without rerunning uncertain/completed units")
	case "review-export":
		fs.StringVar(&out, "out", "", "campaign directory")
		fs.StringVar(&destination, "destination", "", "new blind review directory")
	case "review-import":
		fs.StringVar(&out, "out", "", "campaign directory")
		fs.StringVar(&scores, "scores", "", "review revision JSON")
	case "report":
		fs.StringVar(&out, "out", "", "campaign directory")
		fs.StringVar(&format, "format", "json", "json or markdown")
		fs.StringVar(&revision, "review-revision", "", "explicit review revision ID")
		fs.BoolVar(&requirePass, "require-pass", false, "exit 1 unless live release passes")
	case "cleanup":
		fs.StringVar(&out, "out", "", "owned campaign directory")
		fs.StringVar(&campaign, "campaign", "", "expected campaign identity")
	default:
		return bad(errors.New("unknown evaluation command"))
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return bad(errors.New("unexpected positional arguments"))
	}
	if args[0] == "validate" || args[0] == "run" {
		if manifest == "" {
			return bad(errors.New("--manifest required"))
		}
		m, err := loadManifest(manifest)
		if err != nil {
			return bad(err)
		}
		if args[0] == "validate" {
			fmt.Fprintln(stdout, "Manifest valid:", m.ID)
			return 0
		}
		if out == "" {
			return bad(errors.New("--out required"))
		}
		credentials := map[string]string{}
		if live {
			for _, model := range m.Models {
				credentials[model.CredentialRef] = os.Getenv(model.CredentialRef)
			}
			if err := evaluation.PreflightLive(m, ceiling, credentials); err != nil {
				return bad(err)
			}
		} else if ceiling != 0 {
			return bad(errors.New("spending ceiling requires --live"))
		}
		c, err := evaluation.RunCampaign(ctx, m, engine.NewEvaluationRunner(), evaluation.CampaignOptions{Out: out, Live: live, MaxCostUSD: ceiling, Resume: resume, Credentials: credentials})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Campaign %s: %s (%s)\n", c.ID, c.Phase, out)
		r := evaluation.Evaluate(c, evaluation.ReviewRevision{})
		if r.Regression == evaluation.Fail {
			return 1
		}
		return 0
	}
	if out == "" {
		return bad(errors.New("--out required"))
	}
	if args[0] == "cleanup" {
		if campaign == "" {
			return bad(errors.New("--campaign required"))
		}
		if err := evaluation.CleanupOwned(out, campaign); err != nil {
			return bad(err)
		}
		fmt.Fprintln(stdout, "Removed owned campaign", campaign)
		return 0
	}
	c, err := evaluation.LoadCampaign(out)
	if err != nil {
		return bad(err)
	}
	switch args[0] {
	case "review-export":
		if destination == "" {
			return bad(errors.New("--destination required"))
		}
		if err := evaluation.ExportReview(c, destination); err != nil {
			return bad(err)
		}
		fmt.Fprintln(stdout, "Blind review exported:", destination)
	case "review-import":
		f, err := os.Open(scores)
		if err != nil {
			return bad(err)
		}
		defer f.Close()
		rev, err := evaluation.RecordReview(out, f)
		if err != nil {
			return bad(err)
		}
		fmt.Fprintln(stdout, "Review revision imported:", rev.ID)
	case "report":
		var rev evaluation.ReviewRevision
		if revision != "" {
			for _, v := range c.Reviews {
				if v.ID == revision {
					rev = v
				}
			}
			if rev.ID == "" {
				return bad(errors.New("unknown review revision"))
			}
		}
		r := evaluation.Evaluate(c, rev)
		if err := evaluation.WriteReport(stdout, r, format); err != nil {
			return bad(err)
		}
		if r.Regression == evaluation.Fail || requirePass && r.LiveRelease != evaluation.Pass {
			return 1
		}
	}
	return 0
}
