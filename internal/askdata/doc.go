// Package askdata defines the shared, storage-independent contracts used by
// the intelligent data-questioning runtime.
//
// Package dependencies must point in one direction:
//
//	registry -> search/graph -> binding -> ir -> compiler -> orchestrator
//
// Cross-cutting packages such as cognition, toolhost, evaluation and http may
// depend on the lowest layer they need, but lower layers must never import a
// higher layer. The root askdata package deliberately contains only immutable
// identifiers, references and evidence contracts. It must not import a
// database adapter, an LLM provider or a query engine.
package askdata
