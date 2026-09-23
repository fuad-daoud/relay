package usage

import (
	"fmt"
	"strings"
)

// Spend is a binding's, or a group's, total. Measured and Estimated are
// separate sums so an estimate never hides inside a measurement; Plan and
// Unknown are round counts, never dollars. Steps and ToolCalls are the
// model steps and tool calls the rounds' streams showed (#323, #324).
type Spend struct {
	Rounds    int     `json:"rounds"`
	Consults  int     `json:"consults"`
	Steps     int     `json:"steps"`
	ToolCalls int     `json:"tool_calls"`
	Measured  float64 `json:"measured"`
	Estimated float64 `json:"estimated"`
	Plan      int     `json:"plan"`
	Unknown   int     `json:"unknown"`
	Tokens    Tokens  `json:"tokens"`
}

// Sum folds usages. isConsult[i] marks entry i as a consult rather than a
// round; nil means every entry is a round. Dollars route by basis; a plan
// lane counts under Plan and its dollars are not cash.
func Sum(us []Usage, isConsult []bool) Spend {
	var s Spend
	for i, u := range us {
		if isConsult != nil && i < len(isConsult) && isConsult[i] {
			s.Consults++
		} else {
			s.Rounds++
		}
		s.Steps += u.Steps
		s.ToolCalls += u.ToolCalls
		s.Tokens = s.Tokens.Add(u.Tokens)
		switch {
		case u.Cost.Plan:
			s.Plan++
		case u.Cost.Basis == Measured:
			s.Measured += u.Cost.USD
		case u.Cost.Basis == Estimated:
			s.Estimated += u.Cost.USD
		default:
			s.Unknown++
		}
	}
	return s
}

// Add is the field-wise sum.
func (s Spend) Add(o Spend) Spend {
	return Spend{
		Rounds: s.Rounds + o.Rounds, Consults: s.Consults + o.Consults,
		Steps: s.Steps + o.Steps, ToolCalls: s.ToolCalls + o.ToolCalls,
		Measured: s.Measured + o.Measured, Estimated: s.Estimated + o.Estimated,
		Plan: s.Plan + o.Plan, Unknown: s.Unknown + o.Unknown,
		Tokens: s.Tokens.Add(o.Tokens),
	}
}

func moneyParts(s Spend) []string {
	var p []string
	if s.Measured > 0 {
		p = append(p, Money(Cost{USD: s.Measured, Basis: Measured}))
	}
	if s.Estimated > 0 {
		p = append(p, Money(Cost{USD: s.Estimated, Basis: Estimated}))
	}
	if s.Plan > 0 {
		p = append(p, fmt.Sprintf("%d plan", s.Plan))
	}
	if s.Unknown > 0 {
		p = append(p, fmt.Sprintf("%d unknown", s.Unknown))
	}
	return p
}

// SpendLine: "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown", with a
// trailing tok cell (#234) when the sum moved tokens: " · 2.1M tok".
func SpendLine(s Spend) string {
	if s.Rounds == 0 && s.Consults == 0 {
		return "no rounds"
	}
	head := fmt.Sprintf("%d rounds", s.Rounds)
	if s.Rounds == 1 {
		head = "1 round"
	}
	if s.Consults > 0 {
		head += fmt.Sprintf(" +%dc", s.Consults)
	}
	out := strings.Join(append([]string{head}, moneyParts(s)...), " · ")
	if total := s.Tokens.Total(); total > 0 {
		out += " · " + ShortTokens(total) + " tok"
	}
	return out
}

// MoneyShort is SpendLine without the rounds part, for the rail card and
// the statusline (#234): "$1.51 · 2.1M tok". Just the tok cell when the
// sum has tokens but no dollars to name.
func MoneyShort(s Spend) string {
	parts := moneyParts(s)
	if total := s.Tokens.Total(); total > 0 {
		parts = append(parts, ShortTokens(total)+" tok")
	}
	return strings.Join(parts, " · ")
}
