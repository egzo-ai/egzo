package hub

import (
	"context"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/engines"
)

// What a project page shows of its templates (their names, and which agents are stale because their template changed) lives in the
// control sidecar, and reading it runs a command there. It changes only when someone runs `egzo project up`, so it is read when a
// page is viewed and kept: until the sidecar is another container, or for a few minutes. A failure is kept for less, so that an old
// sidecar that cannot answer is not asked on every poll of the page.
const (
	publishedTTL     = 5 * time.Minute
	publishedFailTTL = time.Minute
)

type publishedEntry struct {
	published engines.Published
	err       error
	at        time.Time
	controlID string
}

// controlID is the container of the project's control sidecar, from the last scan.
func controlID(p engines.Project) string {
	for _, s := range p.Services {
		if s.Name == "control" {
			return s.ID
		}
	}
	return ""
}

// publishedFor returns the project's templates, from the cache when it is good.
func (h *Hub) publishedFor(ctx context.Context, engine string, p engines.Project) (engines.Published, bool) {
	key := projectKey{engine, p.Name}
	id := controlID(p)
	h.pubMu.Lock()
	entry := h.pub[key]
	h.pubMu.Unlock()
	ttl := publishedTTL
	if entry != nil && entry.err != nil {
		ttl = publishedFailTTL
	}
	if entry == nil || entry.controlID != id || time.Since(entry.at) > ttl {
		fresh := &publishedEntry{at: time.Now(), controlID: id}
		fresh.err = h.call(ctx, engine, func(e engines.Engine) (err error) {
			fresh.published, err = e.Published(ctx, p.Name)
			return err
		})
		h.pubMu.Lock()
		h.pub[key] = fresh
		h.pubMu.Unlock()
		entry = fresh
	}
	return entry.published, entry.err == nil
}

// withTemplates fills in the templates of a project and which of its instances are stale, when the sidecar could say.
func withTemplates(p engines.Project, published engines.Published, ok bool) engines.Project {
	if !ok {
		return p
	}
	p.Templates = published.Names()
	agents := make([]engines.Agent, len(p.Agents))
	for i, a := range p.Agents {
		agents[i] = a
		if a.TemplateHash != "" {
			hash, found := published.Templates[a.Template]
			agents[i].Stale = !found || hash != a.TemplateHash
		}
	}
	p.Agents = agents
	return p
}
