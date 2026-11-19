package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestServeURL(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Query", r.URL.Query().Get("q"))
		w.Header().Set("X-Auth", r.Header.Get("Authorization"))
		w.WriteHeader(201)
		w.Write(body)
	})
	h.HandleFunc("GET /bin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0, 1, 2, 255})
	})
	h.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Error("buffered writer must not claim to flush")
		}
	})

	ev := events.LambdaFunctionURLRequest{RawPath: "/echo", RawQueryString: "q=kora",
		Headers: map[string]string{"authorization": "Bearer opk_x", "content-type": "application/json"},
		Body:    base64.StdEncoding.EncodeToString([]byte(`{"a":1}`)), IsBase64Encoded: true}
	ev.RequestContext.HTTP.Method = "POST"
	resp, err := serveURL(context.Background(), h, ev)
	if err != nil || resp.StatusCode != 201 || resp.Body != `{"a":1}` || resp.IsBase64Encoded ||
		resp.Headers["X-Query"] != "kora" || resp.Headers["X-Auth"] != "Bearer opk_x" {
		t.Fatalf("echo: %+v %v", resp, err)
	}

	ev = events.LambdaFunctionURLRequest{RawPath: "/bin"}
	ev.RequestContext.HTTP.Method = "GET"
	resp, _ = serveURL(context.Background(), h, ev)
	if data, _ := base64.StdEncoding.DecodeString(resp.Body); !resp.IsBase64Encoded || string(data) != "\x00\x01\x02\xff" {
		t.Fatalf("binary: %+v", resp)
	}

	ev.RawPath = "/stream"
	serveURL(context.Background(), h, ev)
}
