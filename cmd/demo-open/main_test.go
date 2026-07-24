package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestBuildDemoURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/label/evaluation_id/values":
			fmt.Fprint(w, `{"status":"success","data":["evaluation-example.com-20260101000000","evaluation-example.com-20260102000000"]}`)
		case "/api/v1/export":
			fmt.Fprint(w, `{"timestamps":[1700000000000,1700000030000]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := buildDemoURL(
		context.Background(),
		server.Client(),
		server.URL,
		"http://127.0.0.1:3000/d/kubecon-kse/alibaba-gpu-2023",
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if got := query.Get("var-evaluation"); got != "evaluation-example.com-20260102000000" {
		t.Fatalf("evaluation = %q", got)
	}
	if got := query.Get("from"); got != "1699999995000" {
		t.Fatalf("from = %q", got)
	}
	if got := query.Get("to"); got != "1700000045000" {
		t.Fatalf("to = %q", got)
	}
}

func TestLatestEvaluationIDRequiresData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":[]}`)
	}))
	defer server.Close()

	_, err := latestEvaluationID(
		context.Background(),
		server.Client(),
		server.URL,
	)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestEvaluationRange(t *testing.T) {
	var start, end string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start = r.URL.Query().Get("start")
		end = r.URL.Query().Get("end")
		fmt.Fprintln(w, `{"timestamps":[1700000003000,1700000001000]}`)
		fmt.Fprintln(w, `{"timestamps":[1700000002000,1700000005000]}`)
	}))
	defer server.Close()

	startedAt, finishedAt, err := evaluationRange(
		context.Background(),
		server.Client(),
		server.URL,
		"evaluation-example.com-20260102000000",
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.UnixMilli(1700000001000); !startedAt.Equal(want) {
		t.Fatalf("startedAt = %v, want %v", startedAt, want)
	}
	if want := time.UnixMilli(1700000005000); !finishedAt.Equal(want) {
		t.Fatalf("finishedAt = %v, want %v", finishedAt, want)
	}
	if start == "" || end == "" {
		t.Fatalf("export range is missing: start=%q end=%q", start, end)
	}
}
