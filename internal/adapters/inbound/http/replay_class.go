package http

// replayClass is what the Idempotency-Key middleware does with the answer of a
// problem type (ADR 0019). It is declared on the entry of the problem catalogue
// (errors.go); writeError tells the capturing writer, the middleware reads it
// and holds no list of slugs. A catalogue entry without a class fails
// TestEveryCatalogueEntryDeclaresAReplayClass.
type replayClass uint8

const (
	// replayUnset is the zero value: no class. No catalogue entry may have it;
	// an answer the catalogue does not know (an unmapped 5xx, a problem written
	// with writeProblem) has it and is released like a RETRYABLE one.
	replayUnset replayClass = iota
	// replayFinal: the answer is a function of the request alone. The refusal
	// is stored under the key and replayed; the handler's writes roll back.
	replayFinal
	// replayOutcome: the command committed state and then refused, and the
	// refusal must be remembered. The handler's writes commit together with the
	// stored answer. No problem type of this service is one today (ADR 0019).
	replayOutcome
	// replayRetryable: nothing was committed and the answer depends on state
	// that can change. Nothing is stored and the key is released: the same
	// request under the same key runs again.
	replayRetryable
)

func (c replayClass) String() string {
	switch c {
	case replayFinal:
		return "FINAL"
	case replayOutcome:
		return "OUTCOME"
	case replayRetryable:
		return "RETRYABLE"
	}
	return "UNSET"
}

// declared reports whether c is one of the three classes.
func (c replayClass) declared() bool {
	return c == replayFinal || c == replayOutcome || c == replayRetryable
}

// replayMarker is implemented by the middleware's capturing writer: writeError
// tells it the class of the problem it is about to write.
type replayMarker interface{ MarkReplay(replayClass) }

// verdict is what the middleware does with one captured answer.
type verdict uint8

const (
	// releaseKey: nothing is stored and everything the request wrote rolls back,
	// the key row included, so the key is free again (a 5xx, a RETRYABLE or an
	// unclassified refusal).
	releaseKey verdict = iota
	// storeRefusal: the handler's writes roll back and the answer is stored
	// under the key (a FINAL refusal).
	storeRefusal
	// storeAndKeep: the handler's writes commit together with the stored answer
	// (a 2xx, an OUTCOME refusal).
	storeAndKeep
)
