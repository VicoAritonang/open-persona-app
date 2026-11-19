// Command openpersona-lambda runs the hosted OpenPersona API on AWS Lambda.
//
// One function does two jobs:
//
//   - HTTP: a Lambda Function URL (or API Gateway HTTP API) request runs
//     through the same handler as `openpersona-cloud serve`: knowledge,
//     chat, async jobs, pack downloads, MCP, and the API's RPCs over the
//     Connect protocol (unary only: a Function URL is HTTP/1.1 and
//     buffered, so native gRPC and streaming go to the EC2 tier).
//   - Worker: POST /v1/personas/{id}/jobs stores the job in Neon and invokes
//     this same function asynchronously with {"openpersona_job": "job_..."};
//     that invocation runs the answer and writes the result back. Clients
//     poll GET /v1/jobs/{id}.
//
// Packs are pulled from the registry into /tmp on first use and stay there
// while the execution environment is warm. Streaming answers are served by
// the EC2 deployment (openpersona-cloud serve); see deploy/aws.
//
// Build (arm64, provided.al2023):
//
//	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/openpersona-lambda
//
// Environment: NEON_DB_URL (or OPENPERSONA_REGISTRY_URL), OPENAI_API_KEY,
// OPENROUTER_API_KEY, OPENPERSONA_CHAT_MODEL, optional OPENPERSONA_API_KEY
// (an operator key next to the customer keys in the database),
// OPENPERSONA_WORKER_FUNCTION (default: this function), and
// OPENPERSONA_JOB_TIMEOUT (default 10m; keep it under the function timeout).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"go.vicoworks.com/open-persona/llm"
	"go.vicoworks.com/open-persona/persona"
	"go.vicoworks.com/open-persona/server"
	"go.vicoworks.com/open-persona/server/rpc"

	"github.com/VicoAritonang/open-persona-platform/registry"
)

var version = "dev"

// jobEvent is the payload of an async worker invocation.
type jobEvent struct {
	Job string `json:"openpersona_job"`
}

type app struct {
	srv     *server.Server
	handler func(context.Context, events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error)
}

func main() {
	log.SetFlags(0)
	ctx := context.Background()
	a, err := newApp(ctx)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	lambda.Start(a.handle)
}

func newApp(ctx context.Context) (*app, error) {
	llm.LoadDotEnv()
	reg, err := registry.Open(ctx, registry.URLFromEnv(), registry.Options{Schema: os.Getenv("OPENPERSONA_REGISTRY_SCHEMA")})
	if err != nil {
		return nil, err
	}
	jobTimeout := 10 * time.Minute
	if v := os.Getenv("OPENPERSONA_JOB_TIMEOUT"); v != "" {
		if jobTimeout, err = time.ParseDuration(v); err != nil {
			return nil, fmt.Errorf("OPENPERSONA_JOB_TIMEOUT: %w", err)
		}
	}
	srv := &server.Server{
		Catalog:    &registry.Catalog{Registry: reg, Dir: filepath.Join(os.TempDir(), "openpersona-packs"), Configure: configure},
		Auth:       reg,
		Jobs:       reg,
		Usage:      reg,
		JobTimeout: jobTimeout,
		Version:    version,
	}
	for _, o := range strings.Split(os.Getenv("OPENPERSONA_CORS_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			srv.AllowOrigins = append(srv.AllowOrigins, o)
		}
	}
	if key := os.Getenv("OPENPERSONA_API_KEY"); key != "" {
		srv.Auth = server.AnyOf{server.StaticKey(key), reg}
	}
	if spec := os.Getenv("OPENPERSONA_CHAT_MODEL"); spec != "off" {
		if model, err := llm.FromEnv("OPENPERSONA_CHAT_MODEL", llm.DefaultChatModel); err == nil {
			srv.ChatModel = model
		} else {
			log.Printf("note: %v; chat is disabled", err)
		}
	}
	worker := os.Getenv("OPENPERSONA_WORKER_FUNCTION")
	if worker == "" {
		worker = os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	}
	if worker != "" { // on Lambda: jobs run in an async invocation, not a goroutine that would freeze
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, err
		}
		lc := awslambda.NewFromConfig(cfg)
		srv.Dispatch = func(ctx context.Context, id string) error {
			payload, _ := json.Marshal(jobEvent{Job: id})
			_, err := lc.Invoke(ctx, &awslambda.InvokeInput{
				FunctionName: aws.String(worker), InvocationType: types.InvocationTypeEvent, Payload: payload,
			})
			return err
		}
	}
	rpc.Mount(srv, rpc.Options{})
	h := srv.Handler()
	return &app{srv: srv, handler: func(ctx context.Context, ev events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
		return serveURL(ctx, h, ev)
	}}, nil
}

// handle routes an invocation: a worker event or an HTTP request.
func (a *app) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var job jobEvent
	if json.Unmarshal(raw, &job) == nil && job.Job != "" {
		// An error makes Lambda retry the event; RunJob only fails when it
		// could not record the outcome, which is worth a retry.
		return nil, a.srv.RunJob(ctx, job.Job)
	}
	var ev events.LambdaFunctionURLRequest
	if err := json.Unmarshal(raw, &ev); err != nil || ev.RequestContext.HTTP.Method == "" {
		return nil, errors.New("openpersona-lambda: unrecognised event (want a Function URL request or a job)")
	}
	return a.handler(ctx, ev)
}

func configure(p *persona.Persona) error {
	if p.EmbedModel != "" && os.Getenv("OPENPERSONA_EMBED_MODEL") != "off" {
		model, err := llm.New(p.EmbedModel)
		if err != nil {
			log.Printf("note: %v; %s runs without semantic retrieval", err, p.Spec.ID)
			return nil
		}
		p.Embedder = model
	}
	return nil
}
