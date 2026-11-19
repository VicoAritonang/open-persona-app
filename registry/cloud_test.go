package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"go.vicoworks.com/open-persona/server"
)

// Keys, jobs, usage and the catalog against a real Postgres, in a
// throwaway schema (see TestRegistryRoundTrip).
func TestCloudStores(t *testing.T) {
	url := os.Getenv("OPENPERSONA_TEST_REGISTRY_URL")
	if url == "" {
		t.Skip("OPENPERSONA_TEST_REGISTRY_URL is not set")
	}
	ctx := context.Background()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	reg, err := Open(ctx, url, Options{Schema: "openpersona_test_" + hex.EncodeToString(suffix)})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	defer reg.DropSchema(ctx)
	if _, err := reg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	key, k, err := reg.CreateKey(ctx, KeySpec{Owner: "Acme Travel", Name: "prod", Personas: []string{"fina"}, Scopes: []string{server.ScopeKnowledge, server.ScopeChat}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := reg.Authenticate(ctx, key)
	if err != nil || p.KeyID != k.ID || p.Owner != "Acme Travel" || !p.Can(server.ScopeChat, "fina") || p.Can(server.ScopeChat, "kora") || p.Can(server.ScopeDownload, "fina") {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	if _, err := reg.Authenticate(ctx, key+"x"); !errors.Is(err, server.ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}
	if _, _, err := reg.CreateKey(ctx, KeySpec{Owner: "x", Scopes: []string{"admin"}}); err == nil {
		t.Error("unknown scope accepted")
	}

	j := &server.Job{KeyID: k.ID, PersonaID: "fina", Request: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`)}
	if err := reg.CreateJob(ctx, j); err != nil || j.ID == "" || j.Status != server.JobQueued {
		t.Fatalf("create job: %+v %v", j, err)
	}
	claimed, err := reg.ClaimJob(ctx, j.ID, time.Minute)
	if err != nil || claimed == nil || claimed.Status != server.JobRunning || claimed.Attempts != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if again, err := reg.ClaimJob(ctx, j.ID, time.Minute); err != nil || again != nil {
		t.Fatalf("second claim of a running job: %+v %v", again, err)
	}
	if err := reg.FinishJob(ctx, j.ID, json.RawMessage(`{"reply":"ok"}`), ""); err != nil {
		t.Fatal(err)
	}
	got, err := reg.GetJob(ctx, j.ID)
	if err != nil || got.Status != server.JobSucceeded || string(got.Result) != `{"reply": "ok"}` || got.FinishedAt == nil {
		t.Fatalf("finished job: %+v %s %v", got, got.Result, err)
	}
	if again, err := reg.ClaimJob(ctx, j.ID, 0); err != nil || again != nil {
		t.Fatalf("claim of a finished job: %+v %v", again, err)
	}
	if _, err := reg.GetJob(ctx, "job_missing"); !errors.Is(err, server.ErrNotFound) {
		t.Errorf("missing job: %v", err)
	}

	if err := reg.RecordUsage(ctx, server.Usage{KeyID: k.ID, PersonaID: "fina", Op: "chat", PromptTokens: 100, CompletionTokens: 20, Calls: 1}); err != nil {
		t.Fatal(err)
	}
	rows, err := reg.UsageSince(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Owner != "Acme Travel" || rows[0].PromptTokens != 100 {
		t.Fatalf("usage: %+v %v", rows, err)
	}

	if err := reg.RevokeKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Authenticate(ctx, key); !errors.Is(err, server.ErrUnauthorized) {
		t.Errorf("revoked key still works: %v", err)
	}

	// The catalog pulls a pack on first use and serves it from then on.
	if _, _, err := reg.Push(ctx, trainPack(t, "1.0.0", "WHEN spend exceeds Rp 5,000,000 THEN the CFO approves."), PushOptions{}); err != nil {
		t.Fatal(err)
	}
	cat := &Catalog{Registry: reg, Dir: t.TempDir()}
	defer cat.Close()
	fina, err := cat.Persona(ctx, "fina")
	if err != nil || fina.Spec.Version != "1.0.0" {
		t.Fatalf("catalog: %v %v", fina, err)
	}
	if again, _ := cat.Persona(ctx, "fina"); again != fina {
		t.Error("catalog reopened a cached persona")
	}
	if _, err := cat.Persona(ctx, "nobody"); !errors.Is(err, server.ErrNotFound) {
		t.Errorf("unknown persona: %v", err)
	}
	if specs, err := cat.List(ctx); err != nil || len(specs) != 1 || specs[0].ID != "fina" {
		t.Errorf("list: %+v %v", specs, err)
	}
}
