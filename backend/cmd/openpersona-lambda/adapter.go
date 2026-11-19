package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// serveURL runs one Lambda Function URL request (payload format 2.0, also
// used by API Gateway HTTP APIs) through an http.Handler.
func serveURL(ctx context.Context, h http.Handler, ev events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	var body io.Reader = strings.NewReader(ev.Body)
	if ev.IsBase64Encoded {
		data, err := base64.StdEncoding.DecodeString(ev.Body)
		if err != nil {
			return events.LambdaFunctionURLResponse{StatusCode: http.StatusBadRequest, Body: `{"error":"bad base64 body"}`}, nil
		}
		body = bytes.NewReader(data)
	}
	target := ev.RawPath
	if target == "" {
		target = "/"
	}
	if ev.RawQueryString != "" {
		target += "?" + ev.RawQueryString
	}
	req, err := http.NewRequestWithContext(ctx, ev.RequestContext.HTTP.Method, target, body)
	if err != nil {
		return events.LambdaFunctionURLResponse{StatusCode: http.StatusBadRequest, Body: `{"error":"bad request"}`}, nil
	}
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	for _, c := range ev.Cookies {
		req.Header.Add("Cookie", c)
	}
	req.RemoteAddr = ev.RequestContext.HTTP.SourceIP
	req.RequestURI = target
	if host := ev.Headers["host"]; host != "" {
		req.Host = host
	}

	w := &bufferedWriter{header: http.Header{}}
	h.ServeHTTP(w, req)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	resp := events.LambdaFunctionURLResponse{StatusCode: w.status, Headers: map[string]string{}}
	for k, v := range w.header {
		if strings.EqualFold(k, "Set-Cookie") {
			resp.Cookies = append(resp.Cookies, v...)
			continue
		}
		resp.Headers[k] = strings.Join(v, ", ")
	}
	if isText(w.header.Get("Content-Type")) {
		resp.Body = w.body.String()
	} else {
		resp.Body, resp.IsBase64Encoded = base64.StdEncoding.EncodeToString(w.body.Bytes()), true
	}
	return resp, nil
}

func isText(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml")
}

// bufferedWriter collects a response. It deliberately does not implement
// http.Flusher: a buffered Function URL cannot stream, so the server
// answers stream requests with a clear error pointing at the jobs API (or
// the streaming host) instead of pretending to stream.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *bufferedWriter) Header() http.Header { return w.header }

func (w *bufferedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}
