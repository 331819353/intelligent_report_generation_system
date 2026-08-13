// Command askdata-dr-drill validates a disaster-recovery drill receipt and
// recomputes its verdict.
//
// It deliberately performs no recovery of its own. Restoring the control
// database, re-proving the graph projection and re-checking migrations already
// have scripts that refuse to run against the wrong target; what was missing
// was an acceptance artifact tying those steps together in the mandated order
// and stating, in one recomputed verdict, whether the drill may be presented as
// a recovery commitment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/disasterrecovery"
)

const maxReceiptBytes = 4 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("askdata-dr-drill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var receiptPath string
	var printStages bool
	var pretty bool
	flags.StringVar(&receiptPath, "receipt", "", "path to a drill receipt JSON document")
	flags.BoolVar(&printStages, "stages", false, "print the mandated recovery order and exit")
	flags.BoolVar(&pretty, "pretty", true, "indent the JSON output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "askdata-dr-drill does not accept positional arguments")
		return 2
	}
	if printStages {
		for index, stage := range disasterrecovery.OrderedDrillStages() {
			fmt.Fprintf(stdout, "%2d. %s\n", index+1, stage)
		}
		return 0
	}
	if receiptPath == "" {
		fmt.Fprintln(stderr, "-receipt is required (or -stages to print the recovery order)")
		return 2
	}
	raw, err := readReceipt(receiptPath)
	if err != nil {
		fmt.Fprintf(stderr, "read receipt: %v\n", err)
		return 2
	}
	var receipt disasterrecovery.DrillReceipt
	if err := askdata.DecodeStrictJSON(raw, &receipt); err != nil {
		fmt.Fprintf(stderr, "decode receipt: %v\n", err)
		return 2
	}
	if err := receipt.Validate(); err != nil {
		fmt.Fprintf(stderr, "validate receipt: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "write receipt: %v\n", err)
		return 2
	}
	if receipt.Signability.Verdict != disasterrecovery.SignabilitySignable {
		fmt.Fprintf(
			stderr, "%s: this drill is not a recovery commitment; missing: %s\n",
			receipt.Signability.Verdict, strings.Join(receipt.Signability.MissingInputs, ", "),
		)
		return 1
	}
	return 0
}

func readReceipt(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxReceiptBytes {
		return nil, fmt.Errorf("receipt exceeds %d bytes", maxReceiptBytes)
	}
	return raw, nil
}
