// Command report reads monitor's sample CSV plus stage's event CSV, computes
// per-stage avg/max for every sampled metric, and — when given a second
// run's files — prints a side-by-side comparison (e.g. two different
// machines running the same build).
//
// For the common case of "just show me the report after one run," monitor
// itself writes this automatically when it shuts down (see cmd/monitor) —
// this command is for re-generating it later, or for comparing two runs.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/LorneWu/workload-monitor/internal/report"
)

func main() {
	samplesPath := flag.String("samples", "", "monitor sample CSV (required)")
	eventsPath := flag.String("events", "", "stage event CSV (required)")
	samples2Path := flag.String("samples2", "", "second run's monitor sample CSV (optional, enables comparison mode)")
	events2Path := flag.String("events2", "", "second run's stage event CSV")
	label1 := flag.String("label1", "run1", "label for the first run")
	label2 := flag.String("label2", "run2", "label for the second run")
	out := flag.String("out", "", "output path (default: stdout)")
	flag.Parse()

	if *samplesPath == "" || *eventsPath == "" {
		log.Fatal("usage: report -samples samples.csv -events events.csv [-samples2 ... -events2 ... -label1 X -label2 Y] [-out report.md]")
	}

	samples1, cols, err := report.LoadSamples(*samplesPath)
	if err != nil {
		log.Fatalf("load samples: %v", err)
	}
	stages1, err := report.LoadStages(*eventsPath)
	if err != nil {
		log.Fatalf("load stages: %v", err)
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatalf("create output: %v", err)
		}
		defer f.Close()
		w = f
	}

	if *samples2Path == "" || *events2Path == "" {
		report.WriteSingleRun(w, *label1, samples1, cols, stages1)
		return
	}

	samples2, _, err := report.LoadSamples(*samples2Path)
	if err != nil {
		log.Fatalf("load samples2: %v", err)
	}
	stages2, err := report.LoadStages(*events2Path)
	if err != nil {
		log.Fatalf("load stages2: %v", err)
	}
	report.WriteComparison(w, *label1, *label2, samples1, samples2, cols, stages1, stages2)
}
