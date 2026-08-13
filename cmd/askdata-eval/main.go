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
	"intelligent-report-generation-system/internal/askdata/evaluation/goldenset"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
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
	var suite string
	var trigger string
	var narrativeReviewsPath string
	var expectedReportHash string
	var pretty bool
	flags.StringVar(&fixturePath, "fixture", "", "path to an explicitly synthetic fixture JSON file")
	flags.StringVar(&retrievalCasesPath, "retrieval-cases", "", "path to retrieval gold/candidate cases without question text")
	flags.StringVar(&retrievalMode, "retrieval-mode", string(evaluation.RetrievalModeANN), "retrieval mode: ANN or EXACT")
	flags.StringVar(&suite, "suite", "", "run a platform-owned synthetic suite: time or additivity")
	flags.StringVar(&trigger, "trigger", suites.TimeContractChangedTrigger, "time suite trigger; TIME_CONTRACT_CHANGED forces a full regression")
	flags.StringVar(&narrativeReviewsPath, "narrative-reviews", "", "path to an adjudicated narrative human-review ledger")
	flags.StringVar(&expectedReportHash, "expect-report-hash", "", "pin the narrative review report hash a gate was signed against")
	flags.BoolVar(&pretty, "pretty", true, "indent the JSON report")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "askdata-eval does not accept positional arguments")
		return 2
	}
	selected := 0
	for _, chosen := range []bool{
		fixturePath != "", retrievalCasesPath != "", suite != "", narrativeReviewsPath != "",
	} {
		if chosen {
			selected++
		}
	}
	if selected > 1 {
		fmt.Fprintln(stderr, "-fixture, -retrieval-cases, -suite and -narrative-reviews are mutually exclusive")
		return 2
	}
	switch {
	case retrievalCasesPath != "":
		return runRetrievalEvaluation(retrievalCasesPath, retrievalMode, pretty, stdout, stderr)
	case suite != "":
		return runGoldenSuite(ctx, suite, trigger, pretty, stdout, stderr)
	case narrativeReviewsPath != "":
		return runNarrativeReview(narrativeReviewsPath, expectedReportHash, pretty, stdout, stderr)
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
	if err := writeReport(stdout, report, pretty); err != nil {
		fmt.Fprintf(stderr, "write fixture report: %v\n", err)
		return 2
	}
	if report.FailedCases > 0 {
		return 1
	}
	return 0
}

// runGoldenSuite runs a platform-owned synthetic suite. These suites need no
// business input: they measure whether the engine applies a declared calendar
// and a declared additivity contract correctly, which is platform truth.
func runGoldenSuite(
	ctx context.Context,
	suite, trigger string,
	pretty bool,
	stdout, stderr io.Writer,
) int {
	switch suite {
	case "time":
		inventory, err := goldenset.NewTimeSuite()
		if err != nil {
			fmt.Fprintf(stderr, "build time golden set: %v\n", err)
			return 2
		}
		report, err := inventory.Run(ctx, trigger)
		if err != nil {
			fmt.Fprintf(stderr, "run time golden set: %v\n", err)
			return 2
		}
		if err := writeReport(stdout, report, pretty); err != nil {
			fmt.Fprintf(stderr, "write time report: %v\n", err)
			return 2
		}
		if !report.GatePassed {
			return 1
		}
	case "additivity":
		inventory, err := goldenset.NewAdditivitySuite()
		if err != nil {
			fmt.Fprintf(stderr, "build additivity golden set: %v\n", err)
			return 2
		}
		report, err := inventory.Run(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "run additivity golden set: %v\n", err)
			return 2
		}
		if err := writeReport(stdout, report, pretty); err != nil {
			fmt.Fprintf(stderr, "write additivity report: %v\n", err)
			return 2
		}
		if !report.GatePassed {
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unknown suite %q: expected time or additivity\n", suite)
		return 2
	}
	return 0
}

// runNarrativeReview recomputes the narrative gate from an adjudicated ledger.
// The reviewers' judgements are business input; the arithmetic over them is not,
// and is therefore never accepted from the file.
func runNarrativeReview(
	path, expectedReportHash string,
	pretty bool,
	stdout, stderr io.Writer,
) int {
	raw, err := readBoundedFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "load narrative reviews: %v\n", err)
		return 2
	}
	var submission goldenset.NarrativeReviewSubmission
	if err := askdata.DecodeStrictJSON(raw, &submission); err != nil {
		fmt.Fprintf(stderr, "decode narrative reviews: %v\n", err)
		return 2
	}
	report, err := goldenset.EvaluateNarrativeSubmission(submission)
	if err != nil {
		fmt.Fprintf(stderr, "evaluate narrative reviews: %v\n", err)
		return 2
	}
	if err := writeReport(stdout, report, pretty); err != nil {
		fmt.Fprintf(stderr, "write narrative report: %v\n", err)
		return 2
	}
	if expectedReportHash != "" {
		if err := suites.RequireNarrativeReviewGate(&report, askdata.ContentHash(expectedReportHash)); err != nil {
			fmt.Fprintf(stderr, "narrative review gate: %v\n", err)
			return 1
		}
		return 0
	}
	if !report.Passed {
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
	if err := writeReport(stdout, report, pretty); err != nil {
		fmt.Fprintf(stderr, "write retrieval report: %v\n", err)
		return 2
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func writeReport(stdout io.Writer, report any, pretty bool) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(report)
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxFixtureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFixtureBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxFixtureBytes)
	}
	return raw, nil
}

func loadRetrievalCases(path string) (evaluation.RetrievalCaseSet, error) {
	raw, err := readBoundedFile(path)
	if err != nil {
		return evaluation.RetrievalCaseSet{}, err
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
	raw, err := readBoundedFile(path)
	if err != nil {
		return testfixture.Set{}, err
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
