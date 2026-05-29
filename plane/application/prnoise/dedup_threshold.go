package prnoise

// DedupCosineThreshold is the cosine-similarity gate for declaring a PR
// a semantic duplicate. The value is quoted directly from ADR-016:
//
//	"Qdrant is reserved for PR deduplication only, with a cosine
//	 similarity threshold of 0.92."
//
// It is intentionally a `const` and not a configuration field; tuning
// it would silently change the ADR-016 contract surface and must be
// done via an ADR amendment. Both the QdrantDeduper implementation and
// the dedup_test threshold-gate cases anchor on this value.
const DedupCosineThreshold = 0.92
