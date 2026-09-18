// Command stage appends a start/end marker for a named build stage to a
// shared CSV log, so a build script can bracket each step (e.g. BaseAll,
// Snapshot.py, SynoUpdate, BuildAll) without needing to compute durations
// itself — `report` correlates these markers against monitor's samples.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 4 || (os.Args[2] != "start" && os.Args[2] != "end") {
		fmt.Fprintln(os.Stderr, "usage: stage <events-file> <start|end> <stage-name>")
		os.Exit(2)
	}
	eventsFile := os.Args[1]
	event := os.Args[2]
	name := os.Args[3]

	f, err := os.OpenFile(eventsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", eventsFile, err)
		os.Exit(1)
	}
	defer f.Close()

	// Write the header only once, when the file is new/empty.
	if fi, err := f.Stat(); err == nil && fi.Size() == 0 {
		fmt.Fprintln(f, "epoch,event,stage")
	}
	fmt.Fprintf(f, "%d,%s,%s\n", time.Now().Unix(), event, name)
}
