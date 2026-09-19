package classify

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsFence(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"```", true},
		{"````", true},
		{"```relay", true},
		{"```go   ", true},
		{"``` relay block", true},
		{"``", false},
		{"`", false},
		{"", false},
		{"   ```", false}, // leading space -> not a fence starting with backtick
		{"```info`bad", false},
		{"normal text", false},
	}

	for _, tc := range cases {
		got := IsFence(tc.line)
		if got != tc.want {
			t.Errorf("IsFence(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestSplit(t *testing.T) {
	t.Run("empty input -> nil", func(t *testing.T) {
		if got := Split(nil); got != nil {
			t.Errorf("Split(nil) = %v, want nil", got)
		}
		if got := Split([]byte("")); got != nil {
			t.Errorf("Split(\"\") = %v, want nil", got)
		}
	})

	t.Run("two prose paragraphs separated by a blank line", func(t *testing.T) {
		text := []byte("first para line 1\nfirst para line 2\n\nsecond para line 1\nsecond para line 2")
		got := Split(text)
		if len(got) != 2 {
			t.Fatalf("expected 2 paragraphs, got %d", len(got))
		}
		if got[0].Kind != KindProse || got[0].Text != "first para line 1\nfirst para line 2" || got[0].Line != 1 || got[0].Lines != 2 || got[0].Index != 0 {
			t.Errorf("unexpected paragraph 0: %+v", got[0])
		}
		if got[1].Kind != KindProse || got[1].Text != "second para line 1\nsecond para line 2" || got[1].Line != 4 || got[1].Lines != 2 || got[1].Index != 1 {
			t.Errorf("unexpected paragraph 1: %+v", got[1])
		}
	})

	t.Run("CRLF input yields same paragraphs as LF", func(t *testing.T) {
		lf := []byte("p1 line 1\np1 line 2\n\np2 line 1\n")
		crlf := []byte("p1 line 1\r\np1 line 2\r\n\r\np2 line 1\r\n")
		gotLF := Split(lf)
		gotCRLF := Split(crlf)
		if !reflect.DeepEqual(gotLF, gotCRLF) {
			t.Errorf("CRLF Split != LF Split:\nLF: %+v\nCRLF: %+v", gotLF, gotCRLF)
		}
	})

	t.Run("whitespace-only line separates", func(t *testing.T) {
		text := []byte("p1\n   \t  \np2")
		got := Split(text)
		if len(got) != 2 {
			t.Fatalf("expected 2 paragraphs, got %d", len(got))
		}
		if got[0].Text != "p1" || got[1].Text != "p2" {
			t.Errorf("unexpected texts: %+v", got)
		}
	})

	t.Run("fenced body becomes one KindFenced paragraph with fence lines excluded and correct Line", func(t *testing.T) {
		text := []byte("prose\n```\ncode line 1\ncode line 2\n```\nprose after")
		got := Split(text)
		if len(got) != 3 {
			t.Fatalf("expected 3 paragraphs, got %d: %+v", len(got), got)
		}
		if got[1].Kind != KindFenced {
			t.Errorf("expected KindFenced, got %v", got[1].Kind)
		}
		if got[1].Text != "code line 1\ncode line 2" {
			t.Errorf("expected fenced text 'code line 1\\ncode line 2', got %q", got[1].Text)
		}
		if got[1].Line != 3 || got[1].Lines != 2 {
			t.Errorf("expected Line=3, Lines=2, got Line=%d, Lines=%d", got[1].Line, got[1].Lines)
		}
	})

	t.Run("empty fence body yields no paragraph", func(t *testing.T) {
		text := []byte("prose 1\n```\n```\nprose 2")
		got := Split(text)
		if len(got) != 2 {
			t.Fatalf("expected 2 paragraphs, got %d: %+v", len(got), got)
		}
		if got[0].Text != "prose 1" || got[1].Text != "prose 2" {
			t.Errorf("unexpected texts: %+v", got)
		}
	})

	t.Run("unterminated fence runs to EOF", func(t *testing.T) {
		text := []byte("prose\n```\ncode line 1\ncode line 2")
		got := Split(text)
		if len(got) != 2 {
			t.Fatalf("expected 2 paragraphs, got %d", len(got))
		}
		if got[1].Kind != KindFenced || got[1].Text != "code line 1\ncode line 2" || got[1].Line != 3 || got[1].Lines != 2 {
			t.Errorf("unexpected unterminated fence paragraph: %+v", got[1])
		}
	})

	t.Run("fence with an info string", func(t *testing.T) {
		text := []byte("```relay\nstatus: done\n```")
		got := Split(text)
		if len(got) != 1 {
			t.Fatalf("expected 1 paragraph, got %d", len(got))
		}
		if got[0].Kind != KindFenced || got[0].Text != "status: done" || got[0].Line != 2 || got[0].Lines != 1 {
			t.Errorf("unexpected paragraph: %+v", got[0])
		}
	})

	t.Run("blank lines inside a fence are kept in its Text", func(t *testing.T) {
		text := []byte("```\nline 1\n\nline 2\n```")
		got := Split(text)
		if len(got) != 1 {
			t.Fatalf("expected 1 paragraph, got %d", len(got))
		}
		if got[0].Kind != KindFenced || got[0].Text != "line 1\n\nline 2" || got[0].Lines != 3 {
			t.Errorf("unexpected fenced text: %+v", got[0])
		}
	})

	t.Run("text ending with newline yields no trailing empty paragraph", func(t *testing.T) {
		text := []byte("p1\n\np2\n")
		got := Split(text)
		if len(got) != 2 {
			t.Fatalf("expected 2 paragraphs, got %d: %+v", len(got), got)
		}
	})

	t.Run("Index equals slice position", func(t *testing.T) {
		text := []byte("p1\n\n```\ncode\n```\n\np2\n\np3")
		got := Split(text)
		for i, p := range got {
			if p.Index != i {
				t.Errorf("expected Index %d, got %d", i, p.Index)
			}
		}
	})
}

func TestTrimKeepsHeadAndTail(t *testing.T) {
	var paras []Paragraph
	for i := 0; i < 300; i++ {
		paras = append(paras, Paragraph{
			Index: i,
			Kind:  KindProse,
			Text:  "x",
			Line:  i + 1,
			Lines: 1,
		})
	}

	kept, partial := Trim(paras)
	if !partial {
		t.Fatal("expected partial to be true")
	}
	if len(kept) != 120 {
		t.Fatalf("expected 120 kept, got %d", len(kept))
	}
	for i := 0; i < 60; i++ {
		if kept[i].Index != i {
			t.Errorf("kept[%d].Index = %d, want %d", i, kept[i].Index, i)
		}
	}
	for i := 60; i < 120; i++ {
		want := 240 + (i - 60)
		if kept[i].Index != want {
			t.Errorf("kept[%d].Index = %d, want %d", i, kept[i].Index, want)
		}
	}
}

func TestTrimByteBudget(t *testing.T) {
	// 10 paragraphs each of 20,000 bytes. Total 200,000 > MaxStateBytes (96,000).
	// Under budget: 4 paragraphs * 20,000 = 80,000 bytes. 5th would exceed 96,000.
	var paras []Paragraph
	bigText := strings.Repeat("a", 20_000)
	for i := 0; i < 10; i++ {
		paras = append(paras, Paragraph{
			Index: i,
			Kind:  KindProse,
			Text:  bigText,
			Line:  i + 1,
			Lines: 1,
		})
	}

	kept, partial := Trim(paras)
	if !partial {
		t.Fatal("expected partial to be true")
	}
	if len(kept) != 4 {
		t.Fatalf("expected 4 kept, got %d", len(kept))
	}
	// Head-tail alternating: 0, 9, 1, 8
	// In source order: 0, 1, 8, 9
	wantIndexes := []int{0, 1, 8, 9}
	for i, want := range wantIndexes {
		if kept[i].Index != want {
			t.Errorf("kept[%d].Index = %d, want %d", i, kept[i].Index, want)
		}
	}
}

func TestTrimNoop(t *testing.T) {
	paras := []Paragraph{
		{Index: 0, Kind: KindProse, Text: "hello", Line: 1, Lines: 1},
		{Index: 1, Kind: KindProse, Text: "world", Line: 3, Lines: 1},
	}
	kept, partial := Trim(paras)
	if partial {
		t.Fatal("expected partial to be false")
	}
	if !reflect.DeepEqual(kept, paras) {
		t.Errorf("expected kept == paras, got %+v", kept)
	}
}

func TestTrimSingleOversized(t *testing.T) {
	paras := []Paragraph{
		{Index: 0, Kind: KindProse, Text: strings.Repeat("a", MaxStateBytes+1), Line: 1, Lines: 1},
	}
	kept, partial := Trim(paras)
	if !partial {
		t.Fatal("expected partial to be true")
	}
	if len(kept) != 0 {
		t.Fatalf("expected empty kept, got %d", len(kept))
	}
}
