package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.vicoworks.com/open-persona/llm"
	"go.vicoworks.com/open-persona/persona"
)

// The registry test needs a real Postgres. It runs only when
// OPENPERSONA_TEST_REGISTRY_URL is set, in a throwaway schema that is
// dropped afterwards, so it never touches the real registry.

type fakeLLM func(req llm.Request) string

func (f fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{Content: f(req)}, nil
}

func trainPack(t *testing.T, version, policy string) string {
	t.Helper()
	model := fakeLLM(func(req llm.Request) string {
		if strings.HasPrefix(req.Messages[0].Content, "You are writing the core identity") {
			return `{"identity":"You are Fina.","principles":["Follow policy."],"style":"Precise."}`
		}
		return `{"units":[{"kind":"rule","title":"Approval threshold","body":"` + policy + `","tags":["approval"]}]}`
	})
	out := filepath.Join(t.TempDir(), "fina-"+version+".persona")
	tr := persona.Trainer{LLM: model}
	if _, err := tr.Train(context.Background(), persona.Spec{ID: "fina", Version: version, Name: "Fina", Role: "Finance controller"},
		[]persona.Source{{Name: "policy", Text: policy}}, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegistryRoundTrip(t *testing.T) {
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

	if applied, err := reg.Migrate(ctx); err != nil || len(applied) != 2 {
		t.Fatalf("migrate: %v %v", applied, err)
	}
	if applied, err := reg.Migrate(ctx); err != nil || len(applied) != 0 {
		t.Fatalf("second migrate should be a no-op: %v %v", applied, err)
	}

	v1 := trainPack(t, "1.0.0", "WHEN spend exceeds Rp 5,000,000 THEN the CFO approves.")
	v, created, err := reg.Push(ctx, v1, PushOptions{Owner: "lab", Notes: "first"})
	if err != nil || !created || v.Version != "1.0.0" || v.Status != StatusPublished || len(v.SHA256) != 64 {
		t.Fatalf("push: %+v %v %v", v, created, err)
	}
	if _, created, err := reg.Push(ctx, v1, PushOptions{}); err != nil || created {
		t.Fatalf("re-push of identical bytes should be a no-op: %v %v", created, err)
	}
	if _, _, err := reg.Push(ctx, trainPack(t, "1.0.0", "changed"), PushOptions{}); !errors.Is(err, ErrImmutable) {
		t.Fatalf("different bytes under an existing version: %v", err)
	}
	if _, _, err := reg.Push(ctx, trainPack(t, "1.1.0", "WHEN spend exceeds Rp 10,000,000 THEN the CFO approves."), PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.Push(ctx, trainPack(t, "2.0.0-rc.1", "draft"), PushOptions{Status: StatusDraft}); err != nil {
		t.Fatal(err)
	}

	entries, err := reg.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].LatestVersion != "1.1.0" || entries[0].Versions != 3 || entries[0].Visibility != "private" || entries[0].Owner != "lab" {
		t.Fatalf("list: %+v %v", entries, err)
	}

	out := filepath.Join(t.TempDir(), "fina.persona")
	got, downloaded, err := reg.Pull(ctx, "fina", "", out)
	if err != nil || !downloaded || got.Version != "1.1.0" {
		t.Fatalf("pull latest: %+v %v %v", got, downloaded, err)
	}
	if _, downloaded, err := reg.Pull(ctx, "fina", "", out); err != nil || downloaded {
		t.Fatalf("second pull should use the file on disk: %v %v", downloaded, err)
	}
	p, err := persona.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.Version != "1.1.0" {
		t.Errorf("pulled pack is %s", p.Spec.Version)
	}
	p.Close()

	if err := reg.SetStatus(ctx, "fina", "1.1.0", StatusYanked); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Resolve(ctx, "fina", "1.1.0"); !errors.Is(err, ErrYanked) {
		t.Errorf("yanked version: %v", err)
	}
	if v, err := reg.Resolve(ctx, "fina", ""); err != nil || v.Version != "1.0.0" {
		t.Errorf("latest after yank: %+v %v", v, err)
	}
	if _, err := reg.Resolve(ctx, "nobody", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown persona: %v", err)
	}

	st, err := reg.Status(ctx)
	if err != nil || st.Personas != 1 || st.Versions != 3 || st.BlobBytes == 0 {
		t.Errorf("status: %+v %v", st, err)
	}
	var units int
	if err := reg.pool.QueryRow(ctx, reg.q(`SELECT count(*) FROM {{schema}}.persona_units WHERE persona_id='fina' AND version='1.0.0'`)).Scan(&units); err != nil || units != 1 {
		t.Errorf("units copied: %d %v", units, err)
	}
}
