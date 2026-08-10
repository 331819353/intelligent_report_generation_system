package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/evaluation"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

const maxFixtureBytes = 64 << 20

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("askdata-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var fixturePath string
	var retrievalCasesPath string
	var retrievalMode string
	var pretty bool
	flags.StringVar(&fixturePath, "fixture", "", "path to an explicitly synthetic fixture JSON file")
	flags.StringVar(&retrievalCasesPath, "retrieval-cases", "", "path to retrieval gold/candidate cases without question text")
	flags.StringVar(&retrievalMode, "retrieval-mode", string(evaluation.RetrievalModeANN), "retrieval mode: ANN or EXACT")
	flags.BoolVar(&pretty, "pretty", true, "indent the JSON report")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "askdata-eval does not accept positional arguments")
		return 2
	}
	if retrievalCasesPath != "" {
		if fixturePath != "" {
			fmt.Fprintln(stderr, "-fixture and -retrieval-cases are mutually exclusive")
			return 2
		}
		return runRetrievalEvaluation(retrievalCasesPath, retrievalMode, pretty, stdout, stderr)
	}

	fixture := testfixture.Standard()
	if fixturePath != "" {
		loaded, err := loadFixture(fixturePath)
		if err != nil {
			fmt.Fprintf(stderr, "load fixture: %v\n", err)
			return 2
		}
		fixture = loaded
	}
	runner, err := evaluation.NewFixtureRunner(evaluation.NewDeterministicFixturePipeline())
	if err != nil {
		fmt.Fprintf(stderr, "configure fixture runner: %v\n", err)
		return 2
	}
	report, err := runner.Run(ctx, fixture)
	if err != nil {
		fmt.Fprintf(stderr, "run fixture regression: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "write fixture report: %v\n", err)
		return 2
	}
	if report.FailedCases > 0 {
		return 1
	}
	return 0
}

func runRetrievalEvaluation(
	path, rawMode string,
	pretty bool,
	stdout, stderr io.Writer,
) int {
	mode, err := evaluation.ParseRetrievalMode(rawMode)
	if err != nil {
		fmt.Fprintf(stderr, "retrieval mode: %v\n", err)
		return 2
	}
	caseSet, err := loadRetrievalCases(path)
	if err != nil {
		fmt.Fprintf(stderr, "load retrieval cases: %v\n", err)
		return 2
	}
	report, err := evaluation.EvaluateRetrievalRecall(caseSet.Cases, mode)
	if err != nil {
		fmt.Fprintf(stderr, "evaluate retrieval recall: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "write retrieval report: %v\n", err)
		return 2
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func loadRetrievalCases(path string) (evaluation.RetrievalCaseSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return evaluation.RetrievalCaseSet{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxFixtureBytes+1))
	if err != nil {
		return evaluation.RetrievalCaseSet{}, err
	}
	if len(raw) > maxFixtureBytes {
		return evaluation.RetrievalCaseSet{}, fmt.Errorf("retrieval cases exceed %d bytes", maxFixtureBytes)
	}
	var caseSet evaluation.RetrievalCaseSet
	if err := askdata.DecodeStrictJSON(raw, &caseSet); err != nil {
		return evaluation.RetrievalCaseSet{}, err
	}
	if caseSet.SchemaVersion != evaluation.RetrievalEvaluationVersion {
		return evaluation.RetrievalCaseSet{}, fmt.Errorf("schemaVersion must be %q", evaluation.RetrievalEvaluationVersion)
	}
	return caseSet, nil
}

func loadFixture(path string) (testfixture.Set, error) {
	file, err := os.Open(path)
	if err != nil {
		return testfixture.Set{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxFixtureBytes+1))
	if err != nil {
		return testfixture.Set{}, err
	}
	if len(raw) > maxFixtureBytes {
		return testfixture.Set{}, fmt.Errorf("fixture exceeds %d bytes", maxFixtureBytes)
	}
	var fixture testfixture.Set
	if err := askdata.DecodeStrictJSON(raw, &fixture); err != nil {
		return testfixture.Set{}, err
	}
	if err := fixture.Validate(); err != nil {
		return testfixture.Set{}, err
	}
	return fixture, nil
}
