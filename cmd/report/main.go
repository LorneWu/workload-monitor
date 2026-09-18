// Command report reads monitor's sample CSV plus stage's event CSV, computes
// per-stage avg/max for every sampled metric, and — when given a second
// run's files — prints a side-by-side comparison (e.g. two different
// machines running the same build).
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
)

type sample struct {
	epoch  int64
	values map[string]float64
}

type stageWindow struct {
	name       string
	startEpoch int64
	endEpoch   int64
}

func (s stageWindow) duration() int64 { return s.endEpoch - s.startEpoch }

func loadSamples(path string) ([]sample, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 1 {
		return nil, nil, err
	}
	header := rows[0]
	var out []sample
	for _, row := range rows[1:] {
		if len(row) != len(header) {
			continue
		}
		ep, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		vals := make(map[string]float64, len(header)-1)
		for i := 1; i < len(header); i++ {
			v, err := strconv.ParseFloat(row[i], 64)
			if err == nil {
				vals[header[i]] = v
			}
		}
		out = append(out, sample{epoch: ep, values: vals})
	}
	return out, header[1:], nil
}

func loadStages(path string) ([]stageWindow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 1 {
		return nil, err
	}
	starts := map[string]int64{}
	var order []string
	for _, row := range rows[1:] {
		if len(row) != 3 {
			continue
		}
		ep, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		event, name := row[1], row[2]
		if event == "start" {
			if _, seen := starts[name]; !seen {
				order = append(order, name)
			}
			starts[name] = ep
		}
	}
	var out []stageWindow
	// second pass to find matching "end" for each "start", in file order
	ends := map[string]int64{}
	for _, row := range rows[1:] {
		if len(row) != 3 {
			continue
		}
		ep, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		if row[1] == "end" {
			ends[row[2]] = ep
		}
	}
	for _, name := range order {
		out = append(out, stageWindow{name: name, startEpoch: starts[name], endEpoch: ends[name]})
	}
	return out, nil
}

func statsForStage(samples []sample, w stageWindow) map[string][2]float64 {
	stats := map[string][2]float64{} // metric -> [avg, max]
	sums := map[string]float64{}
	maxes := map[string]float64{}
	n := 0
	for _, s := range samples {
		if s.epoch < w.startEpoch || s.epoch > w.endEpoch {
			continue
		}
		n++
		for k, v := range s.values {
			sums[k] += v
			if v > maxes[k] || n == 1 {
				maxes[k] = v
			}
		}
	}
	if n == 0 {
		return stats
	}
	for k, sum := range sums {
		stats[k] = [2]float64{sum / float64(n), maxes[k]}
	}
	return stats
}

func main() {
	samplesPath := flag.String("samples", "", "monitor sample CSV (required)")
	eventsPath := flag.String("events", "", "stage event CSV (required)")
	samples2Path := flag.String("samples2", "", "second run's monitor sample CSV (optional, enables comparison mode)")
	events2Path := flag.String("events2", "", "second run's stage event CSV")
	label1 := flag.String("label1", "run1", "label for the first run")
	label2 := flag.String("label2", "run2", "label for the second run")
	flag.Parse()

	if *samplesPath == "" || *eventsPath == "" {
		log.Fatal("usage: report -samples samples.csv -events events.csv [-samples2 ... -events2 ... -label1 X -label2 Y]")
	}

	samples1, cols, err := loadSamples(*samplesPath)
	if err != nil {
		log.Fatalf("load samples: %v", err)
	}
	stages1, err := loadStages(*eventsPath)
	if err != nil {
		log.Fatalf("load stages: %v", err)
	}
	sort.Strings(cols)

	compare := *samples2Path != "" && *events2Path != ""
	var samples2 []sample
	var stages2 []stageWindow
	if compare {
		samples2, _, err = loadSamples(*samples2Path)
		if err != nil {
			log.Fatalf("load samples2: %v", err)
		}
		stages2, err = loadStages(*events2Path)
		if err != nil {
			log.Fatalf("load stages2: %v", err)
		}
	}

	if !compare {
		fmt.Printf("## Stage durations (%s)\n\n", *label1)
		fmt.Println("| Stage | Duration |")
		fmt.Println("|---|---|")
		for _, w := range stages1 {
			fmt.Printf("| %s | %ds |\n", w.name, w.duration())
		}
		fmt.Println()
		for _, w := range stages1 {
			st := statsForStage(samples1, w)
			fmt.Printf("## %s (%ds)\n\n", w.name, w.duration())
			fmt.Println("| Metric | Avg | Max |")
			fmt.Println("|---|---|---|")
			for _, c := range cols {
				v, ok := st[c]
				if !ok {
					continue
				}
				fmt.Printf("| %s | %.1f | %.1f |\n", c, v[0], v[1])
			}
			fmt.Println()
		}
		return
	}

	fmt.Printf("## Stage duration comparison: %s vs %s\n\n", *label1, *label2)
	fmt.Printf("| Stage | %s | %s | Ratio |\n", *label1, *label2)
	fmt.Println("|---|---|---|---|")
	stage2ByName := map[string]stageWindow{}
	for _, w := range stages2 {
		stage2ByName[w.name] = w
	}
	for _, w1 := range stages1 {
		w2, ok := stage2ByName[w1.name]
		if !ok {
			continue
		}
		ratio := "n/a"
		if w2.duration() > 0 {
			ratio = fmt.Sprintf("%.2fx", float64(w1.duration())/float64(w2.duration()))
		}
		fmt.Printf("| %s | %ds | %ds | %s |\n", w1.name, w1.duration(), w2.duration(), ratio)
	}
	fmt.Println()

	for _, w1 := range stages1 {
		w2, ok := stage2ByName[w1.name]
		if !ok {
			continue
		}
		st1 := statsForStage(samples1, w1)
		st2 := statsForStage(samples2, w2)
		fmt.Printf("## %s — %s: %ds, %s: %ds\n\n", w1.name, *label1, w1.duration(), *label2, w2.duration())
		fmt.Printf("| Metric | %s avg | %s max | %s avg | %s max |\n", *label1, *label1, *label2, *label2)
		fmt.Println("|---|---|---|---|---|")
		for _, c := range cols {
			v1, ok1 := st1[c]
			v2, ok2 := st2[c]
			if !ok1 && !ok2 {
				continue
			}
			fmt.Printf("| %s | %.1f | %.1f | %.1f | %.1f |\n", c, v1[0], v1[1], v2[0], v2[1])
		}
		fmt.Println()
	}
}
