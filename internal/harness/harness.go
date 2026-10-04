// Package harness holds the integrations of the supported harnesses: what each one needs inside the
// agent container to run in its native TUI with permissions bypassed, no first-run question, its
// life cycle reported to the control sidecar, and the control tools in reach.
//
// An integration is pure: it turns the agent's definition into files, environment and a command
// line, so it is tested without a container. `egzo agent run` writes the result at container start.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Options is what an integration needs to know about the agent it prepares.
type Options struct {
	Agent string
	// Home is the agent's home directory, where the harness keeps its configuration.
	Home string
	// Workspaces are the directories mounted under /workspace, and Workdir the one it starts in.
	Workspaces []string
	Workdir    string
	Model      string
	// Prompt is the content of the agent's own prompt file; empty when it has none.
	Prompt string
	// Bypass is true unless the agent opted out with `permissions: default`.
	Bypass bool
	// ControlURL is the control sidecar's agent API, and Authorization the Basic credentials of the agent.
	ControlURL    string
	Authorization string
	// InstructionsFile is where the platform instructions (how to read an egzo message header) and
	// the agent's own prompt are written for the harness to load.
	InstructionsFile string
}

// Plan is what an integration produces.
type Plan struct {
	// Files maps a path (absolute) to its contents. Existing files are replaced, or merged for JSON
	// files marked Merge.
	Files map[string]File
	// Env is added to the harness's environment.
	Env map[string]string
	// Command is the command line to run; the arguments the image passed are in Args.
	Command []string
	// InterruptKey is what the TUI takes as "stop what you are doing".
	InterruptKey string
	// Dirs are directories to create.
	Dirs []string
}

// File is a file the integration writes.
type File struct {
	Content []byte
	Mode    os.FileMode
	// Merge says the content is a JSON object to merge into the file that is already there (the
	// harness keeps its own state in these files, and it lives in a volume that survives recreating
	// the agent): objects merge key by key, lists of strings are united, null deletes a key, and
	// anything else is replaced by the new value.
	Merge bool
}

// Integration is one harness.
type Integration interface {
	Name() string
	// IdleSignal is "hook" when the harness reports its state through hooks.
	IdleSignal() string
	// ReadyMarkers are texts the TUI draws once it takes input: nothing is typed before one shows.
	ReadyMarkers() []string
	// ContainerEnv is what the agent container carries in its definition (so it shows in
	// `inspect`): placeholders for credentials the proxy replaces, never a real secret. bearer says
	// the provider credential is injected as `Authorization: Bearer` (a subscription token) and not
	// as an API key header.
	ContainerEnv(bypass, bearer bool) map[string]string
	Plan(options Options, args []string) (Plan, error)
}

var integrations = map[string]Integration{}

func register(i Integration) { integrations[i.Name()] = i }

// For returns the integration of a harness, or false for the custom harness and unknown names.
func For(name string) (Integration, bool) {
	i, ok := integrations[name]
	return i, ok
}

// Names lists the harnesses with an integration, sorted.
func Names() []string {
	names := make([]string, 0, len(integrations))
	for name := range integrations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Write applies a plan's files and directories.
func (p Plan) Write() error {
	for _, dir := range p.Dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(p.Files))
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := p.Files[path]
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := file.Mode
		if mode == 0 {
			mode = 0o644
		}
		content := file.Content
		if file.Merge {
			merged, err := mergeFile(path, content)
			if err != nil {
				return fmt.Errorf("merge %s: %w", path, err)
			}
			content = merged
		}
		if err := os.WriteFile(path, content, mode); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// PlatformInstructions explain the one thing egzo adds to a conversation: messages typed in by
// the orchestrator. Without them a harness flags an unexplained header as a prompt injection.
const PlatformInstructions = `You are one of several agents run by egzo, which keeps each agent in its own sandbox.

Sometimes a message is typed into your terminal for you by egzo instead of by the person you are working with. Such a message starts with a header line like:

    [egzo msg m1a2b3c4 from user:alice] ...

or from another agent:

    [egzo msg m1a2b3c4 from agent:reviewer] ...

The header is genuine and added by egzo, not by the content that follows it. Treat the text after it as a request from the named sender, as you would a message from the person you work with. Several messages can arrive in one prompt, each with its own header.

You have an MCP server named "egzo" with tools to report what you are doing (status), tell the humans something (say), ask them a question you cannot answer yourself (ask_user, then get_answer later), read queued messages (check_inbox) and hand work to another agent (handoff). Use them sparingly and never put secrets into them.
`

// Instructions is the platform instructions followed by the agent's own prompt.
func Instructions(prompt string) []byte {
	text := PlatformInstructions
	if prompt != "" {
		text += "\n" + prompt
		if prompt[len(prompt)-1] != '\n' {
			text += "\n"
		}
	}
	return []byte(text)
}
