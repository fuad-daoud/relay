package usage

import "testing"

func TestSumRoutesByBasis(t *testing.T) {
	us := []Usage{
		{Tokens: Tokens{In: 10}, Cost: Cost{USD: 1.00, Basis: Measured}},
		{Tokens: Tokens{In: 20}, Cost: Cost{USD: 0.25, Basis: Estimated}},
		{Tokens: Tokens{In: 30}, Cost: Cost{USD: 0.15, Basis: Estimated}},
		{Tokens: Tokens{In: 40}, Cost: Cost{USD: 0, Basis: Unknown}},
		{Tokens: Tokens{In: 50}, Cost: Cost{USD: 9.99, Basis: Measured, Plan: true}},
	}
	s := Sum(us, []bool{false, false, true, false, false})
	if s.Rounds != 4 || s.Consults != 1 {
		t.Errorf("rounds/consults = %d/%d, want 4/1", s.Rounds, s.Consults)
	}
	if s.Measured != 1.00 {
		t.Errorf("Measured = %v, want 1.00 (plan dollars excluded)", s.Measured)
	}
	if s.Estimated < 0.399 || s.Estimated > 0.401 {
		t.Errorf("Estimated = %v, want 0.40", s.Estimated)
	}
	if s.Plan != 1 || s.Unknown != 1 {
		t.Errorf("plan/unknown = %d/%d, want 1/1", s.Plan, s.Unknown)
	}
	if s.Tokens.In != 150 {
		t.Errorf("tokens must sum regardless of basis: %+v", s.Tokens)
	}
}

func TestSumPlanIsNotCash(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{USD: 5, Basis: Measured, Plan: true}}}, nil)
	if s.Measured != 0 || s.Estimated != 0 || s.Plan != 1 {
		t.Errorf("a plan round is a count, never dollars: %+v", s)
	}
}

func TestSumNilIsConsult(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{Basis: Unknown}}, {Cost: Cost{Basis: Unknown}}}, nil)
	if s.Rounds != 2 || s.Consults != 0 {
		t.Errorf("nil isConsult means every entry is a round: %+v", s)
	}
}

func TestSpendAdd(t *testing.T) {
	a := Spend{Rounds: 1, Measured: 1, Tokens: Tokens{In: 1}}
	b := Spend{Rounds: 2, Consults: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{Out: 2}}
	got := a.Add(b)
	want := Spend{Rounds: 3, Consults: 1, Measured: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{In: 1, Out: 2}}
	if got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestSpendLine(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Consults: 2, Measured: 1.23, Estimated: 0.40, Plan: 1, Unknown: 2}, "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown"},
		{Spend{Rounds: 1, Measured: 0.30}, "1 round · $0.30"},
		{Spend{Rounds: 3, Unknown: 3}, "3 rounds · 3 unknown"},
		{Spend{Consults: 1, Estimated: 0.02}, "0 rounds +1c · ~$0.02"},
		{Spend{}, "no rounds"},
		{Spend{Rounds: 1, Measured: 0.004}, "1 round · <$0.01"},
	}
	for _, c := range cases {
		if got := SpendLine(c.s); got != c.want {
			t.Errorf("SpendLine(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestMoneyShort(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Measured: 1.23, Estimated: 0.40, Unknown: 2}, "$1.23 · ~$0.40 · 2 unknown"},
		{Spend{Rounds: 2, Plan: 2}, "2 plan"},
		{Spend{Rounds: 2}, ""},
		{Spend{}, ""},
	}
	for _, c := range cases {
		if got := MoneyShort(c.s); got != c.want {
			t.Errorf("MoneyShort(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}
