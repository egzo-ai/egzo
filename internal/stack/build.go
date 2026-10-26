// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
	"github.com/egzo-ai/egzo/internal/termsafe"
	"github.com/egzo-ai/egzo/internal/version"
)

// BuildRepository is where the images `up` builds are tagged. The registry is localhost, so a missing image is never
// looked up on Docker Hub, and Podman names its local builds the same way.
const BuildRepository = "localhost/egzo-build/"

// buildFormat is in every build hash: changing what the hash covers changes it.
const buildFormat = "egzo build 1"

// buildPlan is one agent's build: what the engine is sent, and the tag the content gives it.
type buildPlan struct {
	project, agent string
	root           string // the context directory
	dockerfile     string // relative to root, slash-separated
	args           map[string]string
	target         string
	harnessImage   string // the stock image of the harness, given to the build as config.HarnessImageArg; empty for custom
	files          []contextFile
	Tag            string
}

// contextFile is one entry of the build context, in the order it is sent.
type contextFile struct {
	rel  string // slash-separated, relative to the context
	path string
	info fs.FileInfo
	link string
}

// planBuild reads an agent's build context and computes its tag. The tag covers everything the engine is sent and
// every input of the build, so an unchanged context keeps its tag and a changed one gets another.
func planBuild(project, dir, name string, agent config.ResolvedAgent, harnessPrefix string) (*buildPlan, error) {
	b := agent.Build
	plan := &buildPlan{
		project: project, agent: name, root: config.BuildContext(dir, b.Context),
		dockerfile: filepath.ToSlash(filepath.Clean(filepath.FromSlash(b.Dockerfile))), args: b.Args, target: b.Target,
	}
	if agent.Harness != "custom" {
		plan.harnessImage = harnessImage(agent.Harness, harnessPrefix)
	}
	files, err := readContext(plan.root, plan.dockerfile)
	if err != nil {
		return nil, fmt.Errorf("agent %q: build context %s: %w", name, b.Context, err)
	}
	plan.files = files

	h := sha256.New()
	field := func(s string) { fmt.Fprintf(h, "%d:%s", len(s), s) }
	for _, s := range []string{buildFormat, project, name, plan.dockerfile, plan.target, plan.harnessImage} {
		field(s)
	}
	for _, key := range sortedKeys(plan.args) {
		field(key)
		field(plan.args[key])
	}
	for _, f := range files {
		field(f.rel)
		field(fmt.Sprintf("%o", f.info.Mode()))
		field(f.link)
		if f.info.Mode().IsRegular() {
			if err := hashFile(h, f); err != nil {
				return nil, fmt.Errorf("agent %q: build context %s: %w", name, b.Context, err)
			}
		}
	}
	plan.Tag = BuildRepository + buildName(project, name) + ":" + hex.EncodeToString(h.Sum(nil))[:16]
	return plan, nil
}

func hashFile(h io.Writer, f contextFile) error {
	file, err := os.Open(f.path)
	if err != nil {
		return err
	}
	defer file.Close()
	fmt.Fprintf(h, "%d:", f.info.Size())
	_, err = io.Copy(h, file)
	return err
}

// buildName is the repository of an agent's images: the project and agent names, which are identifiers, with what a
// repository name does not allow (an underscore next to a hyphen, a trailing separator) made into hyphens.
func buildName(project, agent string) string {
	return strings.TrimRight(strings.ReplaceAll(project+"-"+agent, "_", "-"), "-")
}

// harnessImage is the stock image of a harness at the CLI's version.
func harnessImage(harness, prefix string) string {
	if prefix == "" {
		prefix = DefaultHarnessPrefix
	}
	return prefix + harness + ":" + version.Version
}

// readContext lists the context directory less what its .dockerignore excludes, as the docker CLI sends it: the
// Dockerfile and the .dockerignore are always sent, and a symbolic link is sent as a link.
func readContext(root, dockerfile string) ([]contextFile, error) {
	var patterns []string
	if f, err := os.Open(filepath.Join(root, ".dockerignore")); err == nil {
		patterns, err = ignorefile.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf(".dockerignore: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf(".dockerignore: %w", err)
	}
	var files []contextFile
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != dockerfile && rel != ".dockerignore" {
			excluded, err := matcher.MatchesOrParentMatches(rel)
			if err != nil {
				return fmt.Errorf(".dockerignore: %w", err)
			}
			if excluded {
				// an exclusion further down can bring back a file under an excluded directory
				if entry.IsDir() && !matcher.Exclusions() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := contextFile{rel: rel, path: path, info: info}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if file.link, err = os.Readlink(path); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return nil // sockets, devices and pipes are not sent
		}
		files = append(files, file)
		return nil
	})
	return files, err
}

// writeContext writes the context as the tar the engine reads, every entry owned by root as the docker CLI sends it.
func writeContext(w io.Writer, files []contextFile) error {
	tw := tar.NewWriter(w)
	for _, f := range files {
		header, err := tar.FileInfoHeader(f.info, f.link)
		if err != nil {
			return err
		}
		header.Name = f.rel
		if f.info.IsDir() {
			header.Name += "/"
		}
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if f.info.Mode().IsRegular() {
			file, err := os.Open(f.path)
			if err != nil {
				return err
			}
			_, err = io.CopyN(tw, file, f.info.Size())
			file.Close()
			if err != nil {
				return fmt.Errorf("%s changed while it was sent: %w", f.rel, err)
			}
		}
	}
	return tw.Close()
}

// missingBase finds the image a build could not fetch. A build sent to Docker's build endpoint has no session, and the
// session is what lets BuildKit fetch from a registry, so an image the engine does not have fails the build with this
// message. The text is not an API: a spec pins it.
var missingBase = regexp.MustCompile(`failed to resolve source metadata for (\S+): no active sessions`)

// syntaxLine is a Dockerfile's parser directive, which Docker can only follow with a session.
var syntaxLine = regexp.MustCompile(`(?im)^#\s*syntax\s*=`)

// buildImage builds an agent's image on the engine and tags it. The harness image is pulled first, with the credentials
// spawn uses; any other image the build reports missing is pulled the same way and the build is tried again.
func buildImage(ctx context.Context, c *engine.Client, plan *buildPlan, out io.Writer) error {
	if plan.harnessImage != "" {
		if err := ensureImage(ctx, c, plan.harnessImage); err != nil {
			return err
		}
	}
	pulled := map[string]bool{}
	for {
		err := runBuild(ctx, c, plan, out)
		match := missingBase.FindStringSubmatch(errorText(err))
		if match == nil {
			return err
		}
		if dockerfile, _ := os.ReadFile(filepath.Join(plan.root, filepath.FromSlash(plan.dockerfile))); syntaxLine.Match(dockerfile) {
			return fmt.Errorf("agent %q: the `# syntax=` line of %s is not supported: egzo builds through the engine's API, where "+
				"Docker cannot fetch a Dockerfile frontend. Remove the line: the built-in parser handles heredocs and RUN --mount", plan.agent, plan.dockerfile)
		}
		if pulled[match[1]] {
			return err
		}
		pulled[match[1]] = true
		fmt.Fprintf(out, "  pull %s, which the build starts from, and build again\n", match[1])
		if err := ensureImage(ctx, c, match[1]); err != nil {
			return fmt.Errorf("agent %q: the build starts from %s: %w", plan.agent, match[1], err)
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// runBuild sends the context to the engine's build endpoint and prints what the build says. Docker runs it on BuildKit
// (version 2, never the legacy builder), Podman on buildah, which ignores the version.
func runBuild(ctx context.Context, c *engine.Client, plan *buildPlan, out io.Writer) error {
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(writeContext(writer, plan.files)) }()
	defer reader.Close()
	args := map[string]*string{}
	for key, value := range plan.args {
		args[key] = &value
	}
	if plan.harnessImage != "" {
		args[config.HarnessImageArg] = &plan.harnessImage
	}
	response, err := c.API.ImageBuild(ctx, reader, build.ImageBuildOptions{
		Tags:        []string{plan.Tag},
		Dockerfile:  plan.dockerfile,
		BuildArgs:   args,
		Target:      plan.target,
		Labels:      map[string]string{engine.LabelProject: plan.project, engine.LabelService: plan.agent},
		Version:     build.BuilderBuildKit,
		Remove:      true,
		ForceRemove: true,
	})
	if err != nil {
		return fmt.Errorf("agent %q: build: %w", plan.agent, err)
	}
	defer response.Body.Close()
	if err := readBuild(response.Body, &indented{w: out}); err != nil {
		return fmt.Errorf("agent %q: build: %w", plan.agent, err)
	}
	return nil
}

// readBuild prints a build's progress and returns its error. Podman streams lines of text; Docker's BuildKit streams
// its progress as base64 protobuf, of which the steps and their output are printed.
func readBuild(r io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(r)
	seen := map[string]bool{}
	for {
		var message struct {
			Stream      string          `json:"stream"`
			ID          string          `json:"id"`
			Aux         json.RawMessage `json:"aux"`
			Error       string          `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("read the build's output: %w", err)
		}
		switch {
		case message.Error != "" || message.ErrorDetail.Message != "":
			text := message.ErrorDetail.Message
			if text == "" {
				text = message.Error
			}
			return errors.New(strings.TrimSpace(text))
		case message.Stream != "":
			io.WriteString(out, termsafe.Block(message.Stream))
		case message.ID == "moby.buildkit.trace":
			var encoded string
			if json.Unmarshal(message.Aux, &encoded) == nil {
				if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
					printTrace(data, seen, out)
				}
			}
		}
	}
}

// printTrace prints one BuildKit StatusResponse: each step once, by its name, and what the steps wrote. The fields are
// those of BuildKit's control API (StatusResponse: vertexes 1, logs 3; Vertex: digest 1, name 3; VertexLog: msg 4).
func printTrace(data []byte, seen map[string]bool, out io.Writer) {
	for _, vertex := range protoFields(data, 1) {
		digest, name := string(first(protoFields(vertex, 1))), string(first(protoFields(vertex, 3)))
		if name != "" && !seen[digest] {
			seen[digest] = true
			fmt.Fprintf(out, "%s\n", termsafe.Line(name))
		}
	}
	for _, log := range protoFields(data, 3) {
		io.WriteString(out, termsafe.Block(string(first(protoFields(log, 4)))))
	}
}

// protoFields returns the length-delimited values of one field of a protobuf message, in order.
func protoFields(data []byte, number protowire.Number) [][]byte {
	var values [][]byte
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return values
		}
		data = data[n:]
		if typ == protowire.BytesType && num == number {
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return values
			}
			values = append(values, value)
			data = data[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			return values
		}
		data = data[m:]
	}
	return values
}

func first(values [][]byte) []byte {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

// indented writes every line it is given two spaces in, under the line that announced the build.
type indented struct {
	w       io.Writer
	midLine bool
}

func (i *indented) Write(p []byte) (int, error) {
	var b strings.Builder
	for _, r := range string(p) {
		if !i.midLine {
			b.WriteString("  ")
			i.midLine = true
		}
		b.WriteRune(r)
		if r == '\n' {
			i.midLine = false
		}
	}
	if _, err := io.WriteString(i.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// buildImages builds the images of the agents with a build whose tag is not on the engine, then removes the images
// earlier builds of the project left. It returns whether it built or removed anything.
func buildImages(ctx context.Context, c *engine.Client, project *config.Resolved, dir string, published Published, opts Options, out io.Writer) (bool, error) {
	keep := map[string]bool{}
	changed := false
	for _, name := range sortedKeys(project.Agents) {
		agent := project.Agents[name]
		if agent.Build == nil {
			continue
		}
		tag := published.Templates[name].Image
		keep[tag] = true
		if _, err := c.API.ImageInspect(ctx, tag); err == nil {
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return changed, err
		}
		changed = true
		if opts.DryRun {
			fmt.Fprintf(out, "would build image %s for agent %s\n", tag, name)
			continue
		}
		plan, err := planBuild(project.Name, dir, name, agent, opts.HarnessPrefix)
		if err != nil {
			return changed, err
		}
		if plan.Tag != tag {
			return changed, fmt.Errorf("agent %q: the build context changed while `up` ran: run it again", name)
		}
		fmt.Fprintf(out, "build image %s for agent %s\n", tag, name)
		if err := buildImage(ctx, c, plan, out); err != nil {
			return changed, err
		}
	}
	if opts.DryRun {
		return changed, nil
	}
	removed, err := removeBuilds(ctx, c, project.Name, keep, out)
	return changed || removed, err
}

// removeBuilds removes the tags egzo gave the project's images, except those in keep. An image is removed by its tag,
// so an image someone else tagged too stays under their name.
func removeBuilds(ctx context.Context, c *engine.Client, project string, keep map[string]bool, out io.Writer) (bool, error) {
	images, err := c.API.ImageList(ctx, image.ListOptions{Filters: filters.NewArgs(filters.Arg("label", engine.LabelProject+"="+project))})
	if err != nil {
		return false, fmt.Errorf("list the images built for the project: %w", err)
	}
	var tags []string
	for _, listed := range images {
		for _, tag := range listed.RepoTags {
			if strings.HasPrefix(tag, BuildRepository) && !keep[tag] {
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	for _, tag := range tags {
		fmt.Fprintf(out, "remove image %s\n", tag)
		if _, err := c.API.ImageRemove(ctx, tag, image.RemoveOptions{PruneChildren: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return true, fmt.Errorf("remove image %s: %w", tag, err)
		}
	}
	return len(tags) > 0, nil
}
