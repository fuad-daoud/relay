package classify

import (
	"context"
	"errors"
	"fmt"
)

// Kind says what a paragraph was in the source text.
type Kind string

const (
	KindProse  Kind = "prose"  // a run of non-blank lines outside any fence
	KindFenced Kind = "fenced" // the body of one ``` fence, fence lines excluded
)

// Paragraph is one candidate the classifier judges.
type Paragraph struct {
	Index int // position in the Split output, 0-based; stable across Trim
	Kind  Kind
	Text  string // the paragraph's lines joined by "\n", no trailing newline, CR stripped
	Line  int    // 1-based line number of Text's first line in the source
	Lines int    // number of lines in Text (>= 1)
}

// Request is one scan: every paragraph is judged against the same question.
type Request struct {
	Source     string      // "report" or "dialog"; goes into the state so the model knows what it reads
	Harness    string      // builder harness kind, e.g. "claude"; may be ""
	Paragraphs []Paragraph // already Trim'd; len >= 1
}

// Answers is what a scan returned. Probabilities is parallel to
// Request.Paragraphs: Probabilities[i] is P(Paragraphs[i] is an instruction).
type Answers struct {
	Model         string    // verbatim from the response's "model"
	Probabilities []float64 // len == len(Request.Paragraphs); each in [0,1]
	InputTokens   int       // usage.input_tokens; 0 when absent
}

// Status is what Resolve found, for doctor and for the daemon's one-line note.
type Status struct {
	Configured bool   // a classify block is present in policy
	Provider   string // "jev" when configured
	Model      string // effective model name when configured
	KeySource  string // "env", "file", or "" when no key was found
	KeyPath    string // the file path consulted (always set when Configured), for the doctor message
}

// Classifier answers the injection question over pre-split paragraphs.
type Classifier interface {
	// Judge asks the injection question over req.Paragraphs and returns one
	// probability per paragraph. Errors are transport or configuration, never
	// verdicts. Respects ctx's deadline as the total bound including retry.
	Judge(ctx context.Context, req Request) (Answers, error)
}

var ErrUnavailable = errors.New("classify: unavailable")   // configured, no key; Unavailable.Judge returns it wrapped with the reason
var ErrUnauthorized = errors.New("classify: unauthorized") // HTTP 401; never retried
var ErrBadRequest = errors.New("classify: bad request")    // HTTP 422; never retried; wraps the body's first 200 bytes
var ErrEmpty = errors.New("classify: no paragraphs")       // Judge called with zero paragraphs

// StatusError is any other non-2xx after retries are exhausted.
type StatusError struct {
	Code int
	Body string /* first 200 bytes */
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("classify: http %d: %s", e.Code, e.Body)
}

// Unavailable is a Classifier returned when classify is configured but no key is present.
type Unavailable struct {
	Reason string
}

func (u Unavailable) Judge(context.Context, Request) (Answers, error) {
	return Answers{}, fmt.Errorf("%w: %s", ErrUnavailable, u.Reason)
}
