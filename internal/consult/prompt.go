package consult

import (
	"fmt"
	"strings"
)

// headlessPrompt is the whole prompt a headless consult runs with. Its single
// %s is the question reference: the question itself, delimited and inline when
// it fits InlineAskMax, and "Read: <ask path>" when it does not. The findings
// are the final message rather than a file, because a read-tier process may
// not be able to write one; relevo extracts that message at exit.
const headlessPrompt = "%s\n\nAnswer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relevo records your final message."

// RoundRole is the record label for a consult that asks a closed round's own
// builder. It is a label, not a role table entry: the resumed session already
// fixes what the consult runs as.
const RoundRole = "round"

// roundAskPrompt is the whole prompt a round consult runs with. Its single %s
// is the question reference, exactly as headlessPrompt's is. The origin line is
// a literal because the payload is addressed to a builder but is not a round.
const roundAskPrompt = "relevo: consult · to builder of round %d · about binding %q (not the human)\n\nYou built round %d of this binding. Answer from what you did and why; do not change anything, do not run tools that write.\n\n%s\n\nAnswer as your final message, complete, in markdown; relevo records it."

// InlineAskMax is the largest whole prompt, in bytes, passed inline. The prompt
// is one argv element and Linux caps one argument at 128 KiB; half of that
// leaves room for the wrapper layers. A larger question falls back to a staged
// file.
const InlineAskMax = 64 << 10

// askInlineBlock wraps question for a consult's prompt. The delimiters make
// the question's own markdown unambiguous. One trailing newline is trimmed.
func askInlineBlock(question []byte) string {
	q := strings.TrimSuffix(string(question), "\n")
	return "The question:\n\n-----BEGIN QUESTION-----\n" + q + "\n-----END QUESTION-----"
}

// inlinePrompt renders the prompt with the question inlined and reports
// whether it fits InlineAskMax. render is the consult's template. The decision
// depends only on the question and the template, never on a path. A prompt
// over the limit returns "" and false.
func inlinePrompt(render func(ref string) string, question []byte) (string, bool) {
	p := render(askInlineBlock(question))
	if len(p) <= InlineAskMax {
		return p, true
	}
	return "", false
}

// RoundQuestion renders the round prompt with askPath as the staged fallback:
// the delimited question when the whole prompt fits InlineAskMax, else a "Read:"
// reference to askPath. It reports which form it returned, so the caller can
// stage the question the same way.
func RoundQuestion(round int, name string, question []byte, askPath string) (string, bool) {
	render := func(ref string) string { return fmt.Sprintf(roundAskPrompt, round, name, round, ref) }
	prompt, inline := inlinePrompt(render, question)
	if !inline {
		prompt = render("Read: " + askPath)
	}
	return prompt, inline
}
