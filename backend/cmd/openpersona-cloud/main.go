// Command openpersona-cloud runs and operates the hosted OpenPersona
// platform: the always-on API server (the EC2 tier, which streams and
// serves native gRPC), the persona registry in Postgres (Neon), and the
// customers' API keys.
//
// The library's own CLI (go.vicoworks.com/open-persona/cmd/openpersona)
// trains and serves personas locally; this one holds what only the
// platform needs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"go.vicoworks.com/open-persona/llm"
	"go.vicoworks.com/open-persona/persona"
	"go.vicoworks.com/open-persona/server"
	"go.vicoworks.com/open-persona/server/rpc"

	"github.com/VicoAritonang/open-persona-app/backend/registry"
)

const usage = `openpersona-cloud - the hosted OpenPersona platform

Server (the always-on tier: streaming, native gRPC, MCP, jobs):
  openpersona-cloud serve [-addr :8080] [-packs /var/cache/openpersona]

Registry (packs in Postgres / Neon; URL from NEON_DB_URL):
  openpersona-cloud registry migrate | status | list | versions -id fina
  openpersona-cloud registry set-status -id fina -version 1.0.0 -status deprecated
  openpersona-cloud push -pack fina.persona [-status draft] [-visibility private] [-notes "..."]
  openpersona-cloud pull -id fina [-version 1.0.0] [-out fina.persona]

API keys (stored in the registry database):
  openpersona-cloud keys create -owner "Acme" [-name prod] [-personas kora] [-scopes knowledge,chat,download]
  openpersona-cloud keys list | revoke -id key_... | usage [-since 720h]

  openpersona-cloud version

Environment: NEON_DB_URL (or OPENPERSONA_REGISTRY_URL, DATABASE_URL),
OPENPERSONA_REGISTRY_SCHEMA, OPENAI_API_KEY, OPENROUTER_API_KEY,
OPENPERSONA_CHAT_MODEL, OPENPERSONA_EMBED_MODEL ("off" disables),
OPENPERSONA_API_KEY (an operator key next to the customer keys),
OPENPERSONA_CORS_ORIGINS. Read from .env too.
`

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	llm.LoadDotEnv()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	commands := map[string]func(context.Context, []string) error{
		"serve": serve, "registry": registryCmd, "push": push, "pull": pull, "keys": keysCmd,
		"version": func(context.Context, []string) error { fmt.Println(buildVersion()); return nil },
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := cmd(ctx, os.Args[2:]); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func openRegistry(ctx context.Context) (*registry.Registry, error) {
	return registry.Open(ctx, registry.URLFromEnv(), registry.Options{Schema: os.Getenv("OPENPERSONA_REGISTRY_SCHEMA")})
}

// configurePersona connects the embedding model a pack was trained with,
// when it has vectors and embeddings are not turned off.
func configurePersona(p *persona.Persona) error {
	if p.EmbedModel != "" && os.Getenv("OPENPERSONA_EMBED_MODEL") != "off" {
		if model, err := llm.New(p.EmbedModel); err == nil {
			p.Embedder = model
		} else {
			log.Printf("note: %v; %s runs without semantic retrieval", err, p.Spec.ID)
		}
	}
	return nil
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address (HTTP/1.1 and cleartext HTTP/2 for gRPC; terminate TLS in front)")
	dir := fs.String("packs", filepath.Join(os.TempDir(), "openpersona-packs"), "cache directory packs are pulled into")
	fs.Parse(args)

	reg, err := openRegistry(ctx)
	if err != nil {
		return err
	}
	defer reg.Close()
	cat := &registry.Catalog{Registry: reg, Dir: *dir, Configure: configurePersona}
	defer cat.Close()
	srv := &server.Server{Catalog: cat, Auth: reg, Jobs: reg, Usage: reg, Version: buildVersion(),
		AllowOrigins: splitList(os.Getenv("OPENPERSONA_CORS_ORIGINS"))}
	if key := os.Getenv("OPENPERSONA_API_KEY"); key != "" {
		srv.Auth = server.AnyOf{server.StaticKey(key), reg}
	}
	if model, err := llm.FromEnv("OPENPERSONA_CHAT_MODEL", llm.DefaultChatModel); err == nil {
		srv.ChatModel = model
	} else {
		log.Printf("note: %v; persona chat is disabled", err)
	}
	rpc.Mount(srv, rpc.Options{Reflection: true})

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true) // gRPC behind a TLS-terminating proxy
	// No WriteTimeout: streamed answers can run for minutes.
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), Protocols: protocols,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	log.Printf("openpersona-cloud %s listening on %s (HTTP + gRPC); packs cached in %s", buildVersion(), *addr, *dir)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func push(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	pack := fs.String("pack", "", "persona pack to upload")
	status := fs.String("status", registry.StatusPublished, "published or draft")
	visibility := fs.String("visibility", "", "private, unlisted or public (default: private for a new persona, unchanged otherwise)")
	owner := fs.String("owner", "", "owner recorded when the persona is first registered")
	notes := fs.String("notes", "", "release notes for this version")
	fs.Parse(args)
	if *pack == "" {
		return errors.New("push needs -pack")
	}
	reg, err := openRegistry(ctx)
	if err != nil {
		return err
	}
	defer reg.Close()
	by := os.Getenv("USERNAME")
	if by == "" {
		by = os.Getenv("USER")
	}
	v, created, err := reg.Push(ctx, *pack, registry.PushOptions{Status: *status, Visibility: *visibility, Owner: *owner, Notes: *notes, PublishedBy: by})
	if err != nil {
		return err
	}
	if created {
		log.Printf("pushed %s@%s (%s, %d bytes, sha256 %s)", v.PersonaID, v.Version, v.Status, v.Size, v.SHA256[:12])
	} else {
		log.Printf("%s@%s is already in the registry with identical content; nothing to do", v.PersonaID, v.Version)
	}
	return nil
}

func pull(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	id := fs.String("id", "", "persona id")
	version := fs.String("version", "", "version (default: latest published)")
	out := fs.String("out", "", "output file (default: <id>.persona)")
	fs.Parse(args)
	if *id == "" {
		return errors.New("pull needs -id")
	}
	if *out == "" {
		*out = *id + persona.PackExt
	}
	reg, err := openRegistry(ctx)
	if err != nil {
		return err
	}
	defer reg.Close()
	v, downloaded, err := reg.Pull(ctx, *id, *version, *out)
	if err != nil {
		return err
	}
	if downloaded {
		log.Printf("pulled %s@%s to %s (%d bytes, sha256 verified)", v.PersonaID, v.Version, *out, v.Size)
	} else {
		log.Printf("%s already holds %s@%s", *out, v.PersonaID, v.Version)
	}
	if v.Status == registry.StatusDeprecated {
		log.Printf("warning: %s@%s is deprecated", v.PersonaID, v.Version)
	}
	return nil
}

func registryCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("registry needs a subcommand: migrate, status, list, versions, set-status")
	}
	fs := flag.NewFlagSet("registry "+args[0], flag.ExitOnError)
	id := fs.String("id", "", "persona id")
	version := fs.String("version", "", "version")
	status := fs.String("status", "", "draft, published, deprecated or yanked")
	fs.Parse(args[1:])
	reg, err := openRegistry(ctx)
	if err != nil {
		return err
	}
	defer reg.Close()
	switch args[0] {
	case "migrate":
		applied, err := reg.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			log.Printf("registry schema is up to date")
		} else {
			log.Printf("applied migrations %v", applied)
		}
		return nil
	case "status":
		st, err := reg.Status(ctx)
		if err != nil {
			return err
		}
		printJSON(st)
		if len(st.Migrations) == 0 {
			log.Printf("schema %q is not set up; run: openpersona-cloud registry migrate", st.Schema)
		}
		return nil
	case "list":
		entries, err := reg.List(ctx)
		if err != nil {
			return err
		}
		printJSON(entries)
		return nil
	case "versions":
		if *id == "" {
			return errors.New("registry versions needs -id")
		}
		vs, err := reg.Versions(ctx, *id)
		if err != nil {
			return err
		}
		printJSON(vs)
		return nil
	case "set-status":
		if *id == "" || *version == "" || *status == "" {
			return errors.New("registry set-status needs -id, -version and -status")
		}
		if err := reg.SetStatus(ctx, *id, *version, *status); err != nil {
			return err
		}
		log.Printf("%s@%s is now %s", *id, *version, *status)
		return nil
	}
	return fmt.Errorf("unknown registry subcommand %q", args[0])
}

func keysCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openpersona-cloud keys create|list|revoke|usage")
	}
	fs := flag.NewFlagSet("keys "+args[0], flag.ExitOnError)
	owner := fs.String("owner", "", "who the key is for")
	name := fs.String("name", "", "label, e.g. \"production backend\"")
	personas := fs.String("personas", "*", "comma-separated persona ids, or * for all")
	scopes := fs.String("scopes", "knowledge,chat", "comma-separated: "+strings.Join(server.AllScopes, ","))
	id := fs.String("id", "", "key id (revoke)")
	since := fs.Duration("since", 30*24*time.Hour, "usage window (usage)")
	fs.Parse(args[1:])
	reg, err := openRegistry(ctx)
	if err != nil {
		return err
	}
	defer reg.Close()
	switch args[0] {
	case "create":
		key, k, err := reg.CreateKey(ctx, registry.KeySpec{Owner: *owner, Name: *name, Personas: splitList(*personas), Scopes: splitList(*scopes)})
		if err != nil {
			return err
		}
		printJSON(k)
		fmt.Printf("\nAPI key (shown once, store it now):\n%s\n", key)
	case "list":
		keys, err := reg.ListKeys(ctx)
		if err != nil {
			return err
		}
		printJSON(keys)
	case "revoke":
		if err := reg.RevokeKey(ctx, *id); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", *id)
	case "usage":
		rows, err := reg.UsageSince(ctx, time.Now().Add(-*since))
		if err != nil {
			return err
		}
		printJSON(rows)
	default:
		return fmt.Errorf("unknown keys command %q", args[0])
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
