// Package goldenset holds the platform-owned, synthetic evaluation inventories
// consumed by internal/askdata/evaluation/suites.
//
// These suites are deliberately NOT business golden sets. A business golden set
// answers "what does this company mean by gross margin"; only the business can
// supply that, and HUMAN-001~004 gate it. The suites here answer a different
// question: "given a calendar and an additivity declaration, does the platform
// resolve and compile them correctly". That answer is platform truth, it is
// reproducible without a business domain, and it is exactly why
// suites.TimeSuiteCase requires Synthetic to be true.
//
// Two rules keep the suites honest:
//
//  1. Expectations are declared from the scenario, never produced by the code
//     under test. The time inventory computes its expected interval from the
//     anchor date and the calendar fixture directly; it never calls
//     compiler.Resolve. The additivity inventory declares the AST shape the
//     compiler must produce; it never copies the compiler's own output.
//  2. The adapters call the real production entry points — compiler.Resolve and
//     compiler.Adapt — rather than a reimplementation. A suite that exercises a
//     copy of the engine proves nothing about the engine.
//
// Nothing in this package may be presented as a real domain's fiscal policy,
// metric definition or accuracy result.
package goldenset
