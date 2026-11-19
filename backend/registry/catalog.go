package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.vicoworks.com/open-persona/persona"
)

// Catalog serves the latest published version of every persona in the
// registry, pulling each pack into Dir the first time it is asked for and
// keeping it open. It suits servers whose disk is a cache (AWS Lambda's
// /tmp, a small EC2 instance): a cold start downloads only the packs that
// are actually used, and a pack already on disk with the right hash is not
// downloaded again.
type Catalog struct {
	Registry *Registry
	Dir      string
	// Refresh is how long a resolved version is trusted before the registry
	// is asked again for the latest one (default 5 minutes).
	Refresh time.Duration
	// Configure is called on every newly opened pack, for example to set its
	// Embedder. Optional.
	Configure func(*persona.Persona) error

	mu      sync.Mutex
	entries map[string]*catalogEntry
}

type catalogEntry struct {
	mu      sync.Mutex
	p       *persona.Persona
	sha     string
	checked time.Time
}

// Persona returns the latest published version of id.
func (c *Catalog) Persona(ctx context.Context, id string) (*persona.Persona, error) {
	if err := persona.CheckID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*catalogEntry)
	}
	e := c.entries[id]
	if e == nil {
		e = &catalogEntry{}
		c.entries[id] = e
	}
	c.mu.Unlock()

	e.mu.Lock() // one download per persona at a time; other personas proceed
	defer e.mu.Unlock()
	refresh := c.Refresh
	if refresh <= 0 {
		refresh = 5 * time.Minute
	}
	if e.p != nil && time.Since(e.checked) < refresh {
		return e.p, nil
	}
	v, err := c.Registry.Resolve(ctx, id, "")
	if err != nil {
		if e.p != nil { // registry unreachable: keep serving what we have
			return e.p, nil
		}
		return nil, err
	}
	if e.p != nil && v.SHA256 == e.sha {
		e.checked = time.Now()
		return e.p, nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return nil, err
	}
	// One file per version: requests still holding the previous version
	// keep reading it while the new one opens.
	path := filepath.Join(c.Dir, id+"@"+v.Version+".persona")
	if _, _, err := c.Registry.Pull(ctx, id, v.Version, path); err != nil {
		return nil, err
	}
	p, err := persona.Open(path)
	if err != nil {
		return nil, err
	}
	if c.Configure != nil {
		if err := c.Configure(p); err != nil {
			p.Close()
			return nil, err
		}
	}
	e.p, e.sha, e.checked = p, v.SHA256, time.Now()
	return p, nil
}

// List describes the personas that have a published version, from the
// registry's metadata: nothing is downloaded.
func (c *Catalog) List(ctx context.Context) ([]persona.Spec, error) {
	list, err := c.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	var specs []persona.Spec
	for _, e := range list {
		if e.LatestVersion != "" {
			specs = append(specs, persona.Spec{ID: e.ID, Version: e.LatestVersion, Name: e.Name, Role: e.Role})
		}
	}
	return specs, nil
}

// Close closes every pack the catalog opened.
func (c *Catalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for id, e := range c.entries {
		e.mu.Lock()
		if e.p != nil {
			errs = append(errs, e.p.Close())
		}
		e.mu.Unlock()
		delete(c.entries, id)
	}
	return errors.Join(errs...)
}
