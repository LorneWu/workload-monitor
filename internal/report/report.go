// Package report loads a monitor sample CSV and a stage event CSV, computes
// per-stage avg/max for every metric, and renders the result as Markdown —
// shared by `cmd/report` (manual, supports comparing two runs) and
// `cmd/monitor` (automatic, single run, written when the sampler exits).
package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
)

type Sample struct {
	Epoch  int64
	Values map[string]float64
}

type StageWindow struct {
	Name       string
	StartEpoch int64
	EndEpoch   int64
}

func (w StageWindow) Duration() int64 { return w.EndEpoch - w.StartEpoch }

// LoadSamples reads a monitor-produced CSV and returns each row plus the
// sorted metric column names (everything after the epoch column).
func LoadSamples(path string) ([]Sample, []string, error) {
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
	var out []Sample
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
		out = append(out, Sample{Epoch: ep, Values: vals})
	}
	cols := append([]string(nil), header[1:]...)
	sort.Strings(cols)
	return out, cols, nil
}

// LoadStages reads a stage-produced event CSV and pairs each "start" with
// its matching "end", in the order stages first appeared.
func LoadStages(path string) ([]StageWindow, error) {
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
	ends := map[string]int64{}
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
		switch event {
		case "start":
			if _, seen := starts[name]; !seen {
				order = append(order, name)
			}
			starts[name] = ep
		case "end":
			ends[name] = ep
		}
	}
	var out []StageWindow
	for _, name := range order {
		out = append(out, StageWindow{Name: name, StartEpoch: starts[name], EndEpoch: ends[name]})
	}
	return out, nil
}

// StatsForStage computes [avg, max] per metric across every sample whose
// epoch falls within the stage's [start, end] window.
func StatsForStage(samples []Sample, w StageWindow) map[string][2]float64 {
	stats := map[string][2]float64{}
	sums := map[string]float64{}
	maxes := map[string]float64{}
	n := 0
	for _, s := range samples {
		if s.Epoch < w.StartEpoch || s.Epoch > w.EndEpoch {
			continue
		}
		n++
		for k, v := range s.Values {
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

// WriteSingleRun renders one run's per-stage avg/max as Markdown.
func WriteSingleRun(w io.Writer, label string, samples []Sample, cols []string, stages []StageWindow) {
	fmt.Fprintf(w, "## Stage durations (%s)\n\n", label)
	fmt.Fprintln(w, "| Stage | Duration |")
	fmt.Fprintln(w, "|---|---|")
	for _, s := range stages {
		fmt.Fprintf(w, "| %s | %ds |\n", s.Name, s.Duration())
	}
	fmt.Fprintln(w)
	for _, s := range stages {
		st := StatsForStage(samples, s)
		fmt.Fprintf(w, "## %s (%ds)\n\n", s.Name, s.Duration())
		fmt.Fprintln(w, "| Metric | Avg | Max |")
		fmt.Fprintln(w, "|---|---|---|")
		for _, c := range cols {
			v, ok := st[c]
			if !ok {
				continue
			}
			fmt.Fprintf(w, "| %s | %.1f | %.1f |\n", c, v[0], v[1])
		}
		fmt.Fprintln(w)
	}
}

// WriteComparison renders a side-by-side Markdown comparison of two runs.
func WriteComparison(w io.Writer, label1, label2 string, samples1, samples2 []Sample, cols []string, stages1, stages2 []StageWindow) {
	fmt.Fprintf(w, "## Stage duration comparison: %s vs %s\n\n", label1, label2)
	fmt.Fprintf(w, "| Stage | %s | %s | Ratio |\n", label1, label2)
	fmt.Fprintln(w, "|---|---|---|---|")
	stage2ByName := map[string]StageWindow{}
	for _, s := range stages2 {
		stage2ByName[s.Name] = s
	}
	for _, s1 := range stages1 {
		s2, ok := stage2ByName[s1.Name]
		if !ok {
			continue
		}
		ratio := "n/a"
		if s2.Duration() > 0 {
			ratio = fmt.Sprintf("%.2fx", float64(s1.Duration())/float64(s2.Duration()))
		}
		fmt.Fprintf(w, "| %s | %ds | %ds | %s |\n", s1.Name, s1.Duration(), s2.Duration(), ratio)
	}
	fmt.Fprintln(w)

	for _, s1 := range stages1 {
		s2, ok := stage2ByName[s1.Name]
		if !ok {
			continue
		}
		st1 := StatsForStage(samples1, s1)
		st2 := StatsForStage(samples2, s2)
		fmt.Fprintf(w, "## %s — %s: %ds, %s: %ds\n\n", s1.Name, label1, s1.Duration(), label2, s2.Duration())
		fmt.Fprintf(w, "| Metric | %s avg | %s max | %s avg | %s max |\n", label1, label1, label2, label2)
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, c := range cols {
			v1, ok1 := st1[c]
			v2, ok2 := st2[c]
			if !ok1 && !ok2 {
				continue
			}
			fmt.Fprintf(w, "| %s | %.1f | %.1f | %.1f | %.1f |\n", c, v1[0], v1[1], v2[0], v2[1])
		}
		fmt.Fprintln(w)
	}
}
