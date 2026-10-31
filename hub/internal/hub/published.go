package hub

import "github.com/egzo-ai/egzo/hub/internal/links"

// withTemplates fills in the templates of a project and which of its instances are stale, from what the link pushed with it. A link that
// never read them says nothing: its agents are not stale for that.
func withTemplates(p links.Project, published links.Published) links.Project {
	if published.Templates == nil {
		return p
	}
	p.Templates = validNames(published.Names(), maxTemplates)
	agents := make([]links.Agent, len(p.Agents))
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
