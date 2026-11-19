// Package registry stores persona packs in Postgres (built and tested
// against Neon), so a pack can be distributed from a database as well as
// by copying the .persona file.
//
// The pack file stays the unit of distribution and the source of truth:
// Push uploads its bytes, content-addressed by SHA-256, with its metadata
// and a copy of its knowledge units for review in SQL; Pull downloads a
// version, verifies its hash and writes the same file back to disk, where
// persona.Open serves it read-only as usual. A version, once pushed, is
// immutable.
package registry

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go.vicoworks.com/open-persona/persona"
	"go.vicoworks.com/open-persona/server"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DefaultSchema is the Postgres schema the registry lives in.
const DefaultSchema = "openpersona"

// Errors returned by the registry.
var (
	ErrNotFound  = fmt.Errorf("registry: %w", server.ErrNotFound)
	ErrImmutable = errors.New("registry: this version already exists with different content; bump the version and retrain")
	ErrYanked    = errors.New("registry: this version was yanked")
)

// Statuses of a version.
const (
	StatusDraft      = "draft"      // uploaded, not offered as latest
	StatusPublished  = "published"  // eligible to be latest
	StatusDeprecated = "deprecated" // still downloadable by exact version, never latest
	StatusYanked     = "yanked"     // refused on pull
)

// URLFromEnv returns the database URL from OPENPERSONA_REGISTRY_URL,
// NEON_DB_URL or DATABASE_URL, the first that is set.
func URLFromEnv() string {
	for _, k := range []string{"OPENPERSONA_REGISTRY_URL", "NEON_DB_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Options configures Open.
type Options struct {
	Schema string // default DefaultSchema
}

// Registry is a connection pool to the registry database. It is safe for
// concurrent use.
type Registry struct {
	pool   *pgxpool.Pool
	schema string // already quoted
	name   string
}

var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Open connects to the database at url (a postgres:// URL).
func Open(ctx context.Context, url string, opt Options) (*Registry, error) {
	if url == "" {
		return nil, errors.New("registry: no database URL (set NEON_DB_URL)")
	}
	name := opt.Schema
	if name == "" {
		name = DefaultSchema
	}
	if !schemaPattern.MatchString(name) {
		return nil, fmt.Errorf("registry: invalid schema name %q", name)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("registry: bad database URL: %w", err)
	}
	// Neon's pooled endpoints run PgBouncer in transaction mode; unnamed
	// statements work there whatever its prepared-statement settings.
	if strings.Contains(cfg.ConnConfig.Host, "-pooler") {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 30 * time.Second // a suspended Neon compute takes a few seconds to wake
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("registry: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("registry: connect: %w", err)
	}
	return &Registry{pool: pool, schema: pgx.Identifier{name}.Sanitize(), name: name}, nil
}

// Close releases the pool.
func (r *Registry) Close() { r.pool.Close() }

// q qualifies table names in a statement with the registry's schema.
func (r *Registry) q(sql string) string { return strings.ReplaceAll(sql, "{{schema}}", r.schema) }

// Migrate creates or upgrades the registry schema and returns the
// migrations it applied. It is safe to run concurrently and repeatedly.
func (r *Registry) Migrate(ctx context.Context) ([]int, error) {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for _, s := range []string{
		`SELECT pg_advisory_xact_lock(hashtext('openpersona.migrate:' || $1))`,
		`CREATE SCHEMA IF NOT EXISTS {{schema}}`,
		`CREATE TABLE IF NOT EXISTS {{schema}}.schema_migrations (
		   version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`,
	} {
		var args []any
		if strings.Contains(s, "$1") {
			args = []any{r.name}
		}
		if _, err := tx.Exec(ctx, r.q(s), args...); err != nil {
			return nil, fmt.Errorf("registry: migrate: %w", err)
		}
	}
	applied, err := r.appliedMigrations(ctx, tx)
	if err != nil {
		return nil, err
	}
	var done []int
	for _, f := range files {
		base := strings.TrimPrefix(f, "migrations/")
		num, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("registry: migration %s has no number prefix", base)
		}
		if applied[num] {
			continue
		}
		// A schema created by running the SQL file by hand has the tables
		// but no record of the migration: record it instead of failing.
		if marker := migrationMarkers[num]; marker != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, r.name+"."+marker).Scan(&exists); err != nil {
				return nil, err
			}
			if exists {
				if _, err := tx.Exec(ctx, r.q(`INSERT INTO {{schema}}.schema_migrations(version, name) VALUES ($1, $2)`), num, base+" (baseline)"); err != nil {
					return nil, err
				}
				continue
			}
		}
		body, err := migrations.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, r.q(string(body))); err != nil {
			return nil, fmt.Errorf("registry: migration %s: %w", base, err)
		}
		if _, err := tx.Exec(ctx, r.q(`INSERT INTO {{schema}}.schema_migrations(version, name) VALUES ($1, $2)`), num, base); err != nil {
			return nil, err
		}
		done = append(done, num)
	}
	return done, tx.Commit(ctx)
}

// migrationMarkers names, per migration, the last table it creates: its
// presence means the migration already ran.
var migrationMarkers = map[int]string{1: "persona_units", 2: "usage_events"}

func (r *Registry) appliedMigrations(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[int]bool, error) {
	rows, err := db.Query(ctx, r.q(`SELECT version FROM {{schema}}.schema_migrations`))
	if err != nil {
		return nil, err
	}
	nums, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(nums))
	for _, n := range nums {
		out[n] = true
	}
	return out, nil
}

// Status describes the registry database.
type Status struct {
	Schema     string `json:"schema"`
	Server     string `json:"server"`
	Migrations []int  `json:"migrations"` // applied; empty means run Migrate
	Personas   int    `json:"personas"`
	Versions   int    `json:"versions"`
	BlobBytes  int64  `json:"blob_bytes"`
}

// Status reports the server version, applied migrations and contents.
func (r *Registry) Status(ctx context.Context) (*Status, error) {
	st := &Status{Schema: r.name}
	if err := r.pool.QueryRow(ctx, `SHOW server_version`).Scan(&st.Server); err != nil {
		return nil, err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, r.schema+".schema_migrations").Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return st, nil
	}
	applied, err := r.appliedMigrations(ctx, r.pool)
	if err != nil {
		return nil, err
	}
	for n := range applied {
		st.Migrations = append(st.Migrations, n)
	}
	sort.Ints(st.Migrations)
	if len(st.Migrations) == 0 {
		return st, nil
	}
	err = r.pool.QueryRow(ctx, r.q(`SELECT
	  (SELECT count(*) FROM {{schema}}.personas),
	  (SELECT count(*) FROM {{schema}}.persona_versions),
	  (SELECT coalesce(sum(size_bytes), 0) FROM {{schema}}.pack_blobs)`)).Scan(&st.Personas, &st.Versions, &st.BlobBytes)
	return st, err
}

// Version is one pushed build of a persona.
type Version struct {
	PersonaID   string               `json:"persona_id"`
	Version     string               `json:"version"`
	Status      string               `json:"status"`
	SHA256      string               `json:"sha256"`
	Size        int64                `json:"size_bytes"`
	Format      int32                `json:"format"`
	Spec        persona.Spec         `json:"spec"`
	EmbedModel  string               `json:"embed_model,omitempty"`
	TrainModel  string               `json:"train_model,omitempty"`
	Eval        *persona.EvalSummary `json:"eval,omitempty"`
	TrainedAt   time.Time            `json:"trained_at"`
	PublishedAt time.Time            `json:"published_at"`
	PublishedBy string               `json:"published_by,omitempty"`
	Notes       string               `json:"notes,omitempty"`
}

const versionCols = `persona_id, version, status, sha256, size_bytes, format, spec, embed_model, train_model, eval, coalesce(trained_at, 'epoch'), published_at, published_by, notes`

func scanVersion(row pgx.Row) (*Version, error) {
	var v Version
	var spec, eval []byte
	err := row.Scan(&v.PersonaID, &v.Version, &v.Status, &v.SHA256, &v.Size, &v.Format, &spec, &v.EmbedModel, &v.TrainModel, &eval, &v.TrainedAt, &v.PublishedAt, &v.PublishedBy, &v.Notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(spec, &v.Spec); err != nil {
		return nil, fmt.Errorf("registry: bad spec for %s@%s: %w", v.PersonaID, v.Version, err)
	}
	if len(eval) > 0 {
		v.Eval = new(persona.EvalSummary)
		if err := json.Unmarshal(eval, v.Eval); err != nil {
			return nil, err
		}
	}
	return &v, nil
}

// PushOptions tunes Push.
type PushOptions struct {
	Status      string // StatusPublished (default) or StatusDraft
	Visibility  string // "private" (default for a new persona), "unlisted" or "public"; empty keeps the current one
	Owner       string // recorded when the persona is first registered
	PublishedBy string
	Notes       string // release notes for this version
}

// Push uploads the pack at path as id@version taken from its spec. Pushing
// the same bytes again is a no-op (created is false); pushing different
// bytes under an existing version fails with ErrImmutable.
func (r *Registry) Push(ctx context.Context, path string, opt PushOptions) (v *Version, created bool, err error) {
	if opt.Status == "" {
		opt.Status = StatusPublished
	}
	if opt.Status != StatusPublished && opt.Status != StatusDraft {
		return nil, false, fmt.Errorf("registry: push status must be %q or %q", StatusPublished, StatusDraft)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	p, err := persona.Open(path)
	if err != nil {
		return nil, false, err
	}
	m, err := p.Manifest(ctx)
	var units []persona.Unit
	if err == nil {
		units, err = p.Units(ctx)
	}
	p.Close()
	if err != nil {
		return nil, false, err
	}
	spec := m.Spec
	if spec.Version == "" {
		return nil, false, fmt.Errorf("registry: %s has no version (it predates versioned packs); set \"version\" in the spec and retrain", path)
	}
	if err := errors.Join(persona.CheckID(spec.ID), persona.CheckVersion(spec.Version)); err != nil {
		return nil, false, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	// Serialize pushes of one persona, so the latest-version update below
	// sees every committed version.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('openpersona.push:' || $1))`, spec.ID); err != nil {
		return nil, false, err
	}
	existing, err := scanVersion(tx.QueryRow(ctx, r.q(`SELECT `+versionCols+` FROM {{schema}}.persona_versions WHERE persona_id=$1 AND version=$2`), spec.ID, spec.Version))
	switch {
	case err == nil && existing.SHA256 == hash:
		return existing, false, nil
	case err == nil:
		return nil, false, fmt.Errorf("%w (%s@%s)", ErrImmutable, spec.ID, spec.Version)
	case !errors.Is(err, ErrNotFound):
		return nil, false, err
	}

	newVisibility := opt.Visibility
	if newVisibility == "" {
		newVisibility = "private"
	}
	specJSON, _ := json.Marshal(spec)
	reportJSON, _ := json.Marshal(m.Report)
	var evalJSON []byte
	if m.Eval != nil {
		evalJSON, _ = json.Marshal(m.Eval)
	}
	var trainedAt *time.Time
	if !m.TrainedAt.IsZero() {
		trainedAt = &m.TrainedAt
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO {{schema}}.personas(id, name, role, description, language, owner, visibility) VALUES ($1,$2,$3,$4,$5,$6,$7)
		  ON CONFLICT (id) DO UPDATE SET visibility = CASE WHEN $8 = '' THEN personas.visibility ELSE excluded.visibility END, updated_at = now()`,
			[]any{spec.ID, spec.Name, spec.Role, spec.Description, spec.Language, opt.Owner, newVisibility, opt.Visibility}},
		{`INSERT INTO {{schema}}.pack_blobs(sha256, size_bytes, data) VALUES ($1,$2,$3) ON CONFLICT (sha256) DO NOTHING`,
			[]any{hash, int64(len(data)), data}},
		{`INSERT INTO {{schema}}.persona_versions(persona_id, version, sha256, size_bytes, format, status, spec, core, report, eval, embed_model, train_model, trained_at, published_by, notes)
		  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			[]any{spec.ID, spec.Version, hash, int64(len(data)), m.Format, opt.Status, jsonArg(specJSON), m.Core, jsonArg(reportJSON), jsonArg(evalJSON), m.EmbedModel, m.TrainModel, trainedAt, opt.PublishedBy, opt.Notes}},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, r.q(s.sql), s.args...); err != nil {
			return nil, false, fmt.Errorf("registry: push %s@%s: %w", spec.ID, spec.Version, err)
		}
	}
	rows := make([][]any, len(units))
	for i, u := range units {
		tags := u.Tags
		if tags == nil {
			tags = []string{}
		}
		rows[i] = []any{spec.ID, spec.Version, u.ID, u.Kind, u.Title, u.Body, tags}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{r.name, "persona_units"},
		[]string{"persona_id", "version", "unit_id", "kind", "title", "body", "tags"}, pgx.CopyFromRows(rows)); err != nil {
		return nil, false, fmt.Errorf("registry: push units: %w", err)
	}
	if err := r.refreshLatest(ctx, tx, spec.ID); err != nil {
		return nil, false, err
	}
	v, err = scanVersion(tx.QueryRow(ctx, r.q(`SELECT `+versionCols+` FROM {{schema}}.persona_versions WHERE persona_id=$1 AND version=$2`), spec.ID, spec.Version))
	if err != nil {
		return nil, false, err
	}
	return v, true, tx.Commit(ctx)
}

// refreshLatest points a persona at its highest published version and
// copies that version's descriptive fields onto the persona row.
func (r *Registry) refreshLatest(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, r.q(`SELECT version FROM {{schema}}.persona_versions WHERE persona_id=$1 AND status=$2`), id, StatusPublished)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		_, err = tx.Exec(ctx, r.q(`UPDATE {{schema}}.personas SET latest_version=NULL, updated_at=now() WHERE id=$1`), id)
		return err
	}
	latest := versions[0]
	for _, v := range versions[1:] {
		if persona.CompareVersions(v, latest) > 0 {
			latest = v
		}
	}
	_, err = tx.Exec(ctx, r.q(`UPDATE {{schema}}.personas p SET latest_version=v.version,
	    name=v.spec->>'name', role=v.spec->>'role',
	    description=coalesce(v.spec->>'description', ''), language=coalesce(v.spec->>'language', ''), updated_at=now()
	  FROM {{schema}}.persona_versions v WHERE p.id=$1 AND v.persona_id=$1 AND v.version=$2`), id, latest)
	return err
}

// Resolve finds id@version; an empty version means the latest published one.
// A yanked version is refused.
func (r *Registry) Resolve(ctx context.Context, id, version string) (*Version, error) {
	if version == "" {
		var latest *string
		err := r.pool.QueryRow(ctx, r.q(`SELECT latest_version FROM {{schema}}.personas WHERE id=$1`), id).Scan(&latest)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && latest == nil) {
			return nil, fmt.Errorf("%w: no published version of %q", ErrNotFound, id)
		}
		if err != nil {
			return nil, err
		}
		version = *latest
	}
	v, err := scanVersion(r.pool.QueryRow(ctx, r.q(`SELECT `+versionCols+` FROM {{schema}}.persona_versions WHERE persona_id=$1 AND version=$2`), id, version))
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: %s@%s", ErrNotFound, id, version)
	}
	if err != nil {
		return nil, err
	}
	if v.Status == StatusYanked {
		return nil, fmt.Errorf("%w: %s@%s", ErrYanked, id, version)
	}
	return v, nil
}

// Pull writes id@version (empty = latest) to outPath. If outPath already
// holds exactly that pack, nothing is downloaded (downloaded is false). The
// file is verified against its SHA-256 and opened as a pack before it
// replaces anything at outPath.
func (r *Registry) Pull(ctx context.Context, id, version, outPath string) (v *Version, downloaded bool, err error) {
	v, err = r.Resolve(ctx, id, version)
	if err != nil {
		return nil, false, err
	}
	if have, err := fileSHA256(outPath); err == nil && have == v.SHA256 {
		return v, false, nil
	}
	var data []byte
	if err := r.pool.QueryRow(ctx, r.q(`SELECT data FROM {{schema}}.pack_blobs WHERE sha256=$1`), v.SHA256).Scan(&data); err != nil {
		return nil, false, fmt.Errorf("registry: download %s@%s: %w", v.PersonaID, v.Version, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != v.SHA256 {
		return nil, false, fmt.Errorf("registry: %s@%s: downloaded bytes do not match sha256 %s", v.PersonaID, v.Version, v.SHA256)
	}
	tmp := outPath + ".download"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, false, err
	}
	p, err := persona.Open(tmp)
	if err == nil {
		if p.Spec.ID != v.PersonaID || p.Spec.Version != v.Version {
			err = fmt.Errorf("registry: pack inside %s@%s is %s@%s", v.PersonaID, v.Version, p.Spec.ID, p.Spec.Version)
		}
		p.Close()
	}
	if err == nil {
		err = os.Rename(tmp, outPath)
	}
	if err != nil {
		os.Remove(tmp)
		return nil, false, err
	}
	return v, true, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Entry is a persona in the registry listing.
type Entry struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	Visibility    string    `json:"visibility"`
	Owner         string    `json:"owner,omitempty"`
	LatestVersion string    `json:"latest_version,omitempty"`
	Versions      int       `json:"versions"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// List returns every persona, by ID.
func (r *Registry) List(ctx context.Context) ([]Entry, error) {
	rows, err := r.pool.Query(ctx, r.q(`SELECT p.id, p.name, p.role, p.visibility, p.owner, coalesce(p.latest_version, ''),
	    (SELECT count(*) FROM {{schema}}.persona_versions v WHERE v.persona_id = p.id), p.updated_at
	  FROM {{schema}}.personas p ORDER BY p.id`))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := row.Scan(&e.ID, &e.Name, &e.Role, &e.Visibility, &e.Owner, &e.LatestVersion, &e.Versions, &e.UpdatedAt)
		return e, err
	})
}

// Versions returns every version of a persona, newest first.
func (r *Registry) Versions(ctx context.Context, id string) ([]*Version, error) {
	rows, err := r.pool.Query(ctx, r.q(`SELECT `+versionCols+` FROM {{schema}}.persona_versions WHERE persona_id=$1`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return persona.CompareVersions(out[i].Version, out[j].Version) > 0 })
	return out, nil
}

// SetStatus changes a version's status (for example to deprecate or yank
// it) and recomputes the persona's latest version.
func (r *Registry) SetStatus(ctx context.Context, id, version, status string) error {
	switch status {
	case StatusDraft, StatusPublished, StatusDeprecated, StatusYanked:
	default:
		return fmt.Errorf("registry: unknown status %q", status)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('openpersona.push:' || $1))`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, r.q(`UPDATE {{schema}}.persona_versions SET status=$3 WHERE persona_id=$1 AND version=$2`), id, version, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s@%s", ErrNotFound, id, version)
	}
	if err := r.refreshLatest(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DropSchema deletes the registry schema and everything in it. It exists
// for tests that run against a throwaway schema.
func (r *Registry) DropSchema(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, r.q(`DROP SCHEMA IF EXISTS {{schema}} CASCADE`))
	return err
}

// jsonArg passes JSON to a jsonb parameter as text: in exec mode (used on
// pooled connections) pgx would send a []byte as bytea. nil and "null" become SQL NULL.
func jsonArg(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return string(b)
}
