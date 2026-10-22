// Command sweep deletes Tsuga dashboards, monitors and SLOs left behind by a
// crashed end-to-end run. Usage: go run ./test/e2efull/sweep -run e2e-ab12cd34
//
// Every page of each kind's listing is read.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

var runIDPattern = regexp.MustCompile(`^e2e-([0-9a-f]{8}|[0-9]{19})$`)

func main() {
	runID := flag.String("run", "", "run id to sweep, e.g. e2e-ab12cd34")
	flag.Parse()
	// Names are matched by substring, so anything looser than the format
	// newRunID generates could sweep resources the suite did not create.
	if !runIDPattern.MatchString(*runID) {
		fmt.Fprintln(os.Stderr, "-run must be a run id like e2e-ab12cd34")
		os.Exit(2)
	}

	cfg, err := tsugacli.ConfigFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := tsugacli.New(cfg, os.Stderr)

	deleted := 0
	failures := 0
	for _, kind := range tsugacli.SweptKinds {
		tagged, err := client.ListTaggedWithRun(kind, *runID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failures++
			continue
		}
		for _, item := range tagged {
			if _, err := client.Run(kind, "delete", item.ID); err != nil && !tsugacli.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "deleting %s %s: %v\n", kind, item.ID, err)
				failures++
				continue
			}
			fmt.Printf("deleted %s %s (%s)\n", kind, item.ID, item.Name)
			deleted++
		}
	}
	fmt.Printf("swept %d resources for run %s\n", deleted, *runID)
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "%d operations failed; sweep incomplete\n", failures)
		os.Exit(1)
	}
}
