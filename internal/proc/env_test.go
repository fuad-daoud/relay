package proc

import (
	"reflect"
	"slices"
	"testing"
)

func TestChildEnv(t *testing.T) {
	cases := []struct {
		name   string
		parent []string
		deny   []string
		extra  []string
		want   []string
	}{
		{
			name:   "denied name removed with value",
			parent: []string{"A=1", "TYPESAFE_API_KEY=secret", "B=2"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  nil,
			want:   []string{"A=1", "B=2"},
		},
		{
			name:   "denied name removed when bare NAME",
			parent: []string{"A=1", "TYPESAFE_API_KEY", "B=2"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  nil,
			want:   []string{"A=1", "B=2"},
		},
		{
			name:   "unrelated entries kept in order",
			parent: []string{"FOO=1", "BAR=2", "BAZ=3"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  nil,
			want:   []string{"FOO=1", "BAR=2", "BAZ=3"},
		},
		{
			name:   "NAME_SUFFIX prefix match kept",
			parent: []string{"TYPESAFE_API_KEY_SUFFIX=x", "TYPESAFE_API_KEY=leak", "TYPESAFE_API_KEY2=y"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  nil,
			want:   []string{"TYPESAFE_API_KEY_SUFFIX=x", "TYPESAFE_API_KEY2=y"},
		},
		{
			name:   "extra appended after",
			parent: []string{"A=1", "B=2"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  []string{"C=3", "D=4"},
			want:   []string{"A=1", "B=2", "C=3", "D=4"},
		},
		{
			name:   "extra may set a denied name and it survives",
			parent: []string{"TYPESAFE_API_KEY=parent_val", "A=1"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  []string{"TYPESAFE_API_KEY=extra_val"},
			want:   []string{"A=1", "TYPESAFE_API_KEY=extra_val"},
		},
		{
			name:   "empty parent",
			parent: []string{},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  []string{"X=1"},
			want:   []string{"X=1"},
		},
		{
			name:   "nil deny returns parent contents plus extra",
			parent: []string{"A=1", "TYPESAFE_API_KEY=keep"},
			deny:   nil,
			extra:  []string{"B=2"},
			want:   []string{"A=1", "TYPESAFE_API_KEY=keep", "B=2"},
		},
		{
			name:   "inputs are not mutated",
			parent: []string{"A=1", "TYPESAFE_API_KEY=leak"},
			deny:   []string{"TYPESAFE_API_KEY"},
			extra:  []string{"B=2"},
			want:   []string{"A=1", "B=2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parentCopy := slices.Clone(tc.parent)
			denyCopy := slices.Clone(tc.deny)
			extraCopy := slices.Clone(tc.extra)

			got := ChildEnv(tc.parent, tc.deny, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				if len(got) == 0 && len(tc.want) == 0 {
					// both empty
				} else {
					t.Errorf("ChildEnv() = %v, want %v", got, tc.want)
				}
			}

			if !reflect.DeepEqual(tc.parent, parentCopy) {
				t.Errorf("parent was mutated: got %v, want %v", tc.parent, parentCopy)
			}
			if !reflect.DeepEqual(tc.deny, denyCopy) {
				t.Errorf("deny was mutated: got %v, want %v", tc.deny, denyCopy)
			}
			if !reflect.DeepEqual(tc.extra, extraCopy) {
				t.Errorf("extra was mutated: got %v, want %v", tc.extra, extraCopy)
			}
		})
	}
}
