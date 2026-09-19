package classify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJudgeRequestShape(t *testing.T) {
	var capturedAuth string
	var capturedContentType string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-latest",
			"answers": map[string]any{
				"p0": map[string]any{"type": "noul", "noul": 0.1},
				"p1": map[string]any{"type": "noul", "noul": 0.8},
			},
			"usage": map[string]any{"input_tokens": 100},
		})
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	req := Request{
		Source:  "report",
		Harness: "claude",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "prose line", Line: 1, Lines: 1},
			{Index: 1, Kind: KindFenced, Text: "fenced line", Line: 3, Lines: 1},
		},
	}

	ans, err := client.Judge(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans.Model != "jev-latest" {
		t.Errorf("model = %q, want %q", ans.Model, "jev-latest")
	}

	if capturedAuth != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer k")
	}
	if capturedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", capturedContentType, "application/json")
	}

	if capturedBody["model"] != "jev-latest" {
		t.Errorf("body model = %v, want jev-latest", capturedBody["model"])
	}
	state, ok := capturedBody["state"].(map[string]any)
	if !ok {
		t.Fatalf("state missing or not object: %v", capturedBody["state"])
	}
	if state["source"] != "report" {
		t.Errorf("state.source = %v, want report", state["source"])
	}
	if state["harness"] != "claude" {
		t.Errorf("state.harness = %v, want claude", state["harness"])
	}
	paras, ok := state["paragraphs"].([]any)
	if !ok || len(paras) != 2 {
		t.Fatalf("state.paragraphs invalid: %v", state["paragraphs"])
	}
	p1, ok := paras[1].(map[string]any)
	if !ok || p1["kind"] != "fenced" {
		t.Errorf("paras[1].kind = %v, want fenced", p1["kind"])
	}

	questions, ok := capturedBody["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions missing: %v", capturedBody["questions"])
	}
	q0, ok := questions["p0"].(map[string]any)
	if !ok || q0["type"] != "noul" {
		t.Errorf("questions.p0 type = %v, want noul", q0["type"])
	}
	q1, ok := questions["p1"].(map[string]any)
	if !ok || q1["type"] != "noul" {
		t.Errorf("questions.p1 type = %v, want noul", q1["type"])
	}
	q1Instr, _ := q1["instructions"].(string)
	if !strings.Contains(q1Instr, "`paragraphs[1].text`") {
		t.Errorf("q1 instructions does not contain `paragraphs[1].text`: %q", q1Instr)
	}
	crit, ok := q1["criteria"].(map[string]any)
	if !ok || crit["true"] == nil || crit["false"] == nil {
		t.Errorf("q1 criteria missing true or false: %v", q1["criteria"])
	}
}

func TestJudgeMapsAnswersById(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-model-xyz",
			"answers": map[string]any{
				"p1": map[string]any{"type": "noul", "noul": 0.9},
				"p0": map[string]any{"type": "noul", "noul": 0.1},
			},
			"usage": map[string]any{"input_tokens": 42},
		})
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
			{Index: 1, Kind: KindProse, Text: "para 1", Line: 2, Lines: 1},
		},
	}

	ans, err := client.Judge(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans.Model != "jev-model-xyz" {
		t.Errorf("Model = %q, want %q", ans.Model, "jev-model-xyz")
	}
	if len(ans.Probabilities) != 2 || ans.Probabilities[0] != 0.1 || ans.Probabilities[1] != 0.9 {
		t.Errorf("Probabilities = %v, want [0.1, 0.9]", ans.Probabilities)
	}
	if ans.InputTokens != 42 {
		t.Errorf("InputTokens = %d, want 42", ans.InputTokens)
	}
}

func TestJudgeMissingAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-latest",
			"answers": map[string]any{
				"p0": map[string]any{"type": "noul", "noul": 0.1},
			},
		})
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
			{Index: 1, Kind: KindProse, Text: "para 1", Line: 2, Lines: 1},
		},
	}

	_, err := client.Judge(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "p1") {
		t.Errorf("expected error to mention 'p1', got: %v", err)
	}
}

func TestJudgeRetriesOnce429(t *testing.T) {
	var requestCount int32
	var sleepCalls []time.Duration

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-latest",
			"answers": map[string]any{
				"p0": map[string]any{"type": "noul", "noul": 0.5},
			},
		})
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()
	client.Sleep = func(d time.Duration) {
		sleepCalls = append(sleepCalls, d)
	}

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	ans, err := client.Judge(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ans.Probabilities) != 1 || ans.Probabilities[0] != 0.5 {
		t.Errorf("unexpected probabilities: %v", ans.Probabilities)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("requestCount = %d, want 2", requestCount)
	}
	if len(sleepCalls) != 1 {
		t.Errorf("len(sleepCalls) = %d, want 1", len(sleepCalls))
	}
}

func TestJudgeRetriesOnce529(t *testing.T) {
	var requestCount int32
	var sleepCalls []time.Duration

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(529)
			_, _ = w.Write([]byte("overloaded"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-latest",
			"answers": map[string]any{
				"p0": map[string]any{"type": "noul", "noul": 0.2},
			},
		})
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()
	client.Sleep = func(d time.Duration) {
		sleepCalls = append(sleepCalls, d)
	}

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	ans, err := client.Judge(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ans.Probabilities) != 1 || ans.Probabilities[0] != 0.2 {
		t.Errorf("unexpected probabilities: %v", ans.Probabilities)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("requestCount = %d, want 2", requestCount)
	}
	if len(sleepCalls) != 1 {
		t.Errorf("len(sleepCalls) = %d, want 1", len(sleepCalls))
	}
}

func TestJudge429Twice(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()
	client.Sleep = func(time.Duration) {}

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	_, err := client.Judge(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if statusErr.Code != 429 {
		t.Errorf("statusErr.Code = %d, want 429", statusErr.Code)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("requestCount = %d, want 2", requestCount)
	}
}

func TestJudge401NoRetry(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	_, err := client.Judge(context.Background(), req)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("requestCount = %d, want 1", requestCount)
	}
}

func TestJudge422NoRetry(t *testing.T) {
	var requestCount int32
	bodyMsg := "unprocessable entity details"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(bodyMsg))
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	_, err := client.Judge(context.Background(), req)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("expected ErrBadRequest, got: %v", err)
	}
	if !strings.Contains(err.Error(), bodyMsg) {
		t.Errorf("expected error to contain %q, got: %v", bodyMsg, err)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("requestCount = %d, want 1", requestCount)
	}
}

func TestJudgeRetryDoesNotExceedDeadline(t *testing.T) {
	var sleepCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("too many requests"))
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()
	client.Sleep = func(time.Duration) {
		sleepCalled = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	_, err := client.Judge(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if sleepCalled {
		t.Errorf("Sleep was called, expected immediate return without sleep")
	}
}

func TestJudgeEmpty(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = srv.Client()

	_, err := client.Judge(context.Background(), Request{Source: "report", Paragraphs: nil})
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Errorf("requestCount = %d, want 0", requestCount)
	}
}

func TestJudgeContextCancelled(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(block)
		srv.CloseClientConnections()
		srv.Close()
	}()

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	req := Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}

	_, err := client.Judge(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error to wrap context.Canceled, got: %v", err)
	}
}
