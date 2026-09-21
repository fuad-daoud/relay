package patch

import "testing"

// fixtureSingle is one file, one hunk: a context line followed by an added
// line.
const fixtureSingle = "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

// fixtureTwoFiles is two files: x.go has a context, a delete and two adds;
// y.go (no "---"/"+++" lines, straight from "diff --git" to "@@") has a
// delete and an add.
const fixtureTwoFiles = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"

func fileByPath(t *testing.T, p Patch, path string) File {
	t.Helper()
	for _, f := range p.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file %q in patch (files: %v)", path, p.Files)
	return File{}
}

func newValues(f File) []int {
	var got []int
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			got = append(got, l.New)
		}
	}
	return got
}

func TestParseNumbersPostImage(t *testing.T) {
	p, err := Parse([]byte(fixtureTwoFiles))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	x := fileByPath(t, p, "x.go")
	gotX := newValues(x)
	wantX := []int{1, 0, 2, 3}
	if len(gotX) != len(wantX) {
		t.Fatalf("x.go New values = %v, want %v", gotX, wantX)
	}
	for i := range wantX {
		if gotX[i] != wantX[i] {
			t.Fatalf("x.go New values = %v, want %v", gotX, wantX)
		}
	}

	y := fileByPath(t, p, "y.go")
	gotY := newValues(y)
	wantY := []int{0, 1}
	if len(gotY) != len(wantY) {
		t.Fatalf("y.go New values = %v, want %v", gotY, wantY)
	}
	for i := range wantY {
		if gotY[i] != wantY[i] {
			t.Fatalf("y.go New values = %v, want %v", gotY, wantY)
		}
	}

	if _, l, ok := p.HasLine("x.go", 3); !ok {
		t.Error(`HasLine("x.go", 3) = false, want true`)
	} else if l.Text != "more" {
		t.Errorf(`HasLine("x.go", 3) line text = %q, want "more"`, l.Text)
	}
	if _, _, ok := p.HasLine("x.go", 4); ok {
		t.Error(`HasLine("x.go", 4) = true, want false`)
	}
	if _, _, ok := p.HasLine("x.go", 0); ok {
		t.Error(`HasLine("x.go", 0) = true, want false`)
	}
	if _, _, ok := p.HasLine("z.go", 1); ok {
		t.Error(`HasLine("z.go", 1) = true, want false`)
	}
}

func TestParseCountMismatchIsAnError(t *testing.T) {
	// Header declares +1,3 (three new lines) but only two ('  hello' and
	// '+world') are present.
	bad := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,3 @@\n hello\n+world\n"

	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("Parse: expected error for a hunk whose declared new-line count disagrees with its lines, got nil")
	}
	const want = "hunk at file.txt line 4: expected 3 new lines, found 2"
	if err.Error() != want {
		t.Fatalf("Parse error = %q, want %q", err.Error(), want)
	}
}

func TestAnnotateGutter(t *testing.T) {
	got, err := Annotate([]byte(fixtureSingle))
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	want := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\nfile.txt:1  @@ -1 +1,2 @@\nfile.txt:1  hello\nfile.txt:2 +world\n"
	if string(got) != want {
		t.Fatalf("Annotate(fixtureSingle) =\n%q\nwant\n%q", string(got), want)
	}

	got2, err := Annotate([]byte(fixtureTwoFiles))
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	// Derived from the rule in internal/patch/anchors.go: a header gets its
	// gutter + two spaces + the original header line; a ' '/'+' line gets
	// its gutter + one space + the raw line (kind byte included); a '-'
	// line gets the file's "path:-" gutter + six spaces + the raw line.
	// Gutter widths here: x.go's widest token is "x.go:3" (6 chars); y.go's
	// is "y.go:1" (6 chars) -- so no padding is visible in either file.
	want2 := "diff --git a/x.go b/x.go\n" +
		"--- a/x.go\n" +
		"+++ b/x.go\n" +
		"x.go:1  @@ -1,2 +1,3 @@\n" +
		"x.go:1  context\n" +
		"x.go:-      -old\n" +
		"x.go:2 +new\n" +
		"x.go:3 +more\n" +
		"diff --git a/y.go b/y.go\n" +
		"y.go:1  @@ -1 +1 @@\n" +
		"y.go:-      -a\n" +
		"y.go:1 +b\n"
	if string(got2) != want2 {
		t.Fatalf("Annotate(fixtureTwoFiles) =\n%q\nwant\n%q", string(got2), want2)
	}
}
