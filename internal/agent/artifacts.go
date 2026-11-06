package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	maxArtifacts     = 32
	maxArtifactBytes = 2 * 1024 * 1024
)

type artifact struct {
	ID    string
	Label string
	Body  string
}

var (
	artifactsMu sync.Mutex
	artifacts   = make(map[string]*artifact)
	artifactSeq int64
)

// artifactWrite stores a payload out of context.
type artifactWriteTool struct{}

func (t *artifactWriteTool) Name() string      { return "artifact_write" }
func (t *artifactWriteTool) Aliases() []string { return []string{"store_artifact"} }
func (t *artifactWriteTool) Mutating() bool    { return true }
func (t *artifactWriteTool) Risk() Risk        { return RiskEdit }
func (t *artifactWriteTool) Description() string {
	return "Store a large payload (search dump, log, JSON) out of context and get a short id back."
}
func (t *artifactWriteTool) Schema() map[string]any {
	return object(map[string]any{
		"label": strProp("Human label for this artifact."),
		"body":  strProp("Payload to store."),
	})
}
func (t *artifactWriteTool) Label(args map[string]any) string {
	return "Storing artifact " + Shorten(argString(args, "label"), 30)
}
func (t *artifactWriteTool) DoneLabel(args map[string]any) string {
	return "Stored artifact " + Shorten(argString(args, "label"), 30)
}

func (t *artifactWriteTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	body := argString(args, "body")
	label := argString(args, "label")
	if body == "" {
		return Result{IsError: true, Output: "body is required"}, nil
	}
	if len(body) > maxArtifactBytes {
		return Result{IsError: true, Output: fmt.Sprintf("artifact exceeds %d bytes", maxArtifactBytes)}, nil
	}

	artifactsMu.Lock()
	defer artifactsMu.Unlock()

	// Evict oldest if at capacity.
	for len(artifacts) >= maxArtifacts {
		var oldest string
		for id := range artifacts {
			oldest = id
			break
		}
		delete(artifacts, oldest)
	}

	artifactSeq++
	id := fmt.Sprintf("artifact-%d", artifactSeq)
	artifacts[id] = &artifact{ID: id, Label: label, Body: body}
	return Result{Output: id}, nil
}

// artifactReadTool reads a slice of a stored artifact.
type artifactReadTool struct{}

func (t *artifactReadTool) Name() string      { return "artifact_read" }
func (t *artifactReadTool) Aliases() []string { return []string{"read_artifact"} }
func (t *artifactReadTool) Mutating() bool    { return false }
func (t *artifactReadTool) Risk() Risk        { return RiskEdit }
func (t *artifactReadTool) Description() string {
	return "Read a stored artifact by id, optionally by line range."
}
func (t *artifactReadTool) Schema() map[string]any {
	return object(map[string]any{
		"id":     strProp("Artifact id returned by artifact_write."),
		"offset": intProp("Start line (0-based)."),
		"limit":  intProp("Max lines to return."),
	})
}
func (t *artifactReadTool) Label(args map[string]any) string {
	return "Reading artifact " + Shorten(argString(args, "id"), 20)
}
func (t *artifactReadTool) DoneLabel(args map[string]any) string {
	return "Read artifact " + Shorten(argString(args, "id"), 20)
}

func (t *artifactReadTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	id := argString(args, "id")
	if id == "" {
		return Result{IsError: true, Output: "id is required"}, nil
	}
	artifactsMu.Lock()
	a, ok := artifacts[id]
	artifactsMu.Unlock()
	if !ok {
		return Result{IsError: true, Output: "artifact not found"}, nil
	}
	lines := strings.Split(a.Body, "\n")
	offset := argInt(args, "offset", 0, 0, len(lines))
	limit := argInt(args, "limit", len(lines), 1, len(lines)-offset)
	if offset >= len(lines) {
		return Result{Output: ""}, nil
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	return Result{Output: strings.Join(lines[offset:end], "\n")}, nil
}

// artifactListTool lists stored artifacts.
type artifactListTool struct{}

func (t *artifactListTool) Name() string      { return "artifact_list" }
func (t *artifactListTool) Aliases() []string { return []string{"list_artifacts"} }
func (t *artifactListTool) Mutating() bool    { return false }
func (t *artifactListTool) Risk() Risk        { return RiskEdit }
func (t *artifactListTool) Description() string {
	return "List stored artifacts and their sizes."
}
func (t *artifactListTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *artifactListTool) Label(_ map[string]any) string     { return "Listing artifacts" }
func (t *artifactListTool) DoneLabel(_ map[string]any) string { return "Listed artifacts" }

func (t *artifactListTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	artifactsMu.Lock()
	defer artifactsMu.Unlock()
	if len(artifacts) == 0 {
		return Result{Output: "No artifacts stored."}, nil
	}
	var b strings.Builder
	for _, a := range artifacts {
		b.WriteString(fmt.Sprintf("- %s (%s): %d bytes\n", a.ID, a.Label, len(a.Body)))
	}
	return Result{Output: b.String()}, nil
}
