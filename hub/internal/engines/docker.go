package engines

import (
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/link"
)

// Docker is an Engine on a Docker-compatible API: the code the link runs (internal/link), reached over the hub's own connection
// to the engine.
type Docker struct {
	*link.Engine
	closers []func() // what else to close: an ssh connection
}

// NewDocker wraps an open core engine client.
func NewDocker(c *engine.Client) *Docker { return &Docker{Engine: link.NewEngine(c)} }

// Close closes the engine connection and what it needed.
func (d *Docker) Close() error {
	err := d.Engine.Close()
	for _, f := range d.closers {
		f()
	}
	return err
}
