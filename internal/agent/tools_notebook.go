package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// notebookOutputLimit bounds the output text notebook_read shows per cell.
const notebookOutputLimit = 1200

// loadNotebook reads an .ipynb as generic JSON. A map keeps every field the
// editor does not know about, and json.Marshal sorts keys the way nbformat
// writes them, so an edit changes only the cell it touched.
func loadNotebook(path string) (map[string]any, []any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return nil, nil, fmt.Errorf("not a notebook: %w", err)
	}
	cells, ok := notebook["cells"].([]any)
	if !ok {
		if _, present := notebook["cells"]; present {
			return nil, nil, fmt.Errorf("not a notebook: cells is not a list")
		}
		cells = []any{}
	}
	return notebook, cells, nil
}

func saveNotebook(path string, notebook map[string]any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(notebook); err != nil {
		return err
	}
	return os.WriteFile(path, buffer.Bytes(), 0o644)
}

// notebookText joins a source or text field, which nbformat stores either as
// one string or as a list of lines.
func notebookText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, part := range typed {
			if text, ok := part.(string); ok {
				out.WriteString(text)
			}
		}
		return out.String()
	}
	return ""
}

// notebookLines splits text into the line list nbformat writes: every line
// keeps its newline except the last.
func notebookLines(text string) []any {
	if text == "" {
		return []any{}
	}
	parts := strings.SplitAfter(text, "\n")
	lines := make([]any, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			lines = append(lines, part)
		}
	}
	return lines
}

func cellOutputs(cell map[string]any) string {
	outputs, _ := cell["outputs"].([]any)
	var out strings.Builder
	for _, raw := range outputs {
		output, _ := raw.(map[string]any)
		switch output["output_type"] {
		case "stream":
			out.WriteString(notebookText(output["text"]))
		case "execute_result", "display_data":
			data, _ := output["data"].(map[string]any)
			if text := notebookText(data["text/plain"]); text != "" {
				out.WriteString(text + "\n")
			} else {
				out.WriteString("[non-text output]\n")
			}
		case "error":
			fmt.Fprintf(&out, "%v: %v\n", output["ename"], output["evalue"])
		}
	}
	return strings.TrimSpace(out.String())
}

// notebookReadTool renders a notebook cell by cell, with the index and id
// notebook_edit addresses.
type notebookReadTool struct{}

func (t *notebookReadTool) Name() string      { return "notebook_read" }
func (t *notebookReadTool) Aliases() []string { return []string{"read_notebook"} }
func (t *notebookReadTool) Mutating() bool    { return false }
func (t *notebookReadTool) Risk() Risk        { return RiskEdit }
func (t *notebookReadTool) Label(a map[string]any) string {
	return "Reading notebook " + argString(a, "path")
}
func (t *notebookReadTool) DoneLabel(a map[string]any) string {
	return "Read notebook " + argString(a, "path")
}
func (t *notebookReadTool) Description() string {
	return "Read a Jupyter notebook (.ipynb) as numbered cells with their type, id, source and a short text of their outputs."
}
func (t *notebookReadTool) Schema() map[string]any {
	return object(map[string]any{"path": strProp("Path to the .ipynb file.")}, "path")
}

func (t *notebookReadTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "notebook_path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	_, cells, err := loadNotebook(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if len(cells) == 0 {
		return Result{Output: displayPath(env, path) + " has no cells."}, nil
	}
	var out strings.Builder
	for index, raw := range cells {
		cell, _ := raw.(map[string]any)
		id, _ := cell["id"].(string)
		fmt.Fprintf(&out, "--- cell %d [%v]", index, cell["cell_type"])
		if id != "" {
			fmt.Fprintf(&out, " id=%s", id)
		}
		out.WriteString("\n" + strings.TrimRight(notebookText(cell["source"]), "\n") + "\n")
		if outputs := cellOutputs(cell); outputs != "" {
			out.WriteString("output:\n" + Shorten(outputs, notebookOutputLimit) + "\n")
		}
	}
	return Result{Output: strings.TrimRight(out.String(), "\n")}, nil
}

// notebookEditTool replaces, inserts or deletes one cell of a notebook.
type notebookEditTool struct{}

func (t *notebookEditTool) Name() string      { return "notebook_edit" }
func (t *notebookEditTool) Aliases() []string { return []string{"edit_notebook"} }
func (t *notebookEditTool) Mutating() bool    { return true }
func (t *notebookEditTool) Risk() Risk        { return RiskEdit }
func (t *notebookEditTool) Label(a map[string]any) string {
	return "Editing notebook " + argString(a, "path")
}
func (t *notebookEditTool) DoneLabel(a map[string]any) string {
	return "Edited notebook " + argString(a, "path")
}
func (t *notebookEditTool) Description() string {
	return "Edit one cell of a Jupyter notebook (.ipynb): replace its source, insert a new cell after it, or delete it. Address the cell by cell_id or by its 0-based cell_number from notebook_read. Replacing a code cell clears its stale outputs."
}
func (t *notebookEditTool) Schema() map[string]any {
	return object(map[string]any{
		"path":        strProp("Path to the .ipynb file."),
		"cell_id":     strProp("The id of the cell to edit; for insert, the new cell goes after it."),
		"cell_number": intProp("0-based index of the cell, used when cell_id is not given. For insert, -1 inserts at the top."),
		"new_source":  strProp("The new cell source, for replace and insert."),
		"cell_type":   strProp("code or markdown. Required for insert; for replace it changes the type."),
		"edit_mode":   strProp("replace (default), insert or delete."),
	}, "path")
}

func (t *notebookEditTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "notebook_path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := checkProtectedPath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	notebook, cells, err := loadNotebook(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(argString(args, "edit_mode", "mode")))
	if mode == "" {
		mode = "replace"
	}
	index, err := notebookCellIndex(cells, args, mode == "insert")
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	cellType := strings.ToLower(strings.TrimSpace(argString(args, "cell_type")))
	if cellType != "" && cellType != "code" && cellType != "markdown" && cellType != "raw" {
		return Result{Output: "cell_type must be code, markdown or raw", IsError: true}, nil
	}
	_, hasSource := args["new_source"]
	source := toString(args["new_source"])

	var summary string
	switch mode {
	case "replace":
		if !hasSource {
			return Result{Output: "new_source is required for replace", IsError: true}, nil
		}
		cell, _ := cells[index].(map[string]any)
		cell["source"] = notebookLines(source)
		if cellType != "" {
			cell["cell_type"] = cellType
		}
		shapeCell(cell)
		summary = fmt.Sprintf("Replaced cell %d", index)
	case "insert":
		if cellType == "" {
			return Result{Output: "cell_type is required for insert", IsError: true}, nil
		}
		cell := map[string]any{"cell_type": cellType, "metadata": map[string]any{}, "source": notebookLines(source)}
		if notebookWantsIDs(notebook) {
			cell["id"] = newCellID()
		}
		shapeCell(cell)
		at := index + 1
		cells = append(cells[:at], append([]any{cell}, cells[at:]...)...)
		summary = fmt.Sprintf("Inserted a %s cell at %d", cellType, at)
	case "delete":
		cells = append(cells[:index], cells[index+1:]...)
		summary = fmt.Sprintf("Deleted cell %d", index)
	default:
		return Result{Output: "edit_mode must be replace, insert or delete", IsError: true}, nil
	}
	notebook["cells"] = cells
	backupFile(env, path)
	if err := saveNotebook(path, notebook); err != nil {
		return Result{Output: fmt.Sprintf("write %s: %v", displayPath(env, path), err), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("%s of %s (%d cells now).", summary, displayPath(env, path), len(cells))}, nil
}

// notebookCellIndex resolves the addressed cell. For insert, -1 means before
// the first cell.
func notebookCellIndex(cells []any, args map[string]any, insert bool) (int, error) {
	if id := strings.TrimSpace(argString(args, "cell_id")); id != "" {
		for index, raw := range cells {
			if cell, ok := raw.(map[string]any); ok && cell["id"] == id {
				return index, nil
			}
		}
		return 0, fmt.Errorf("no cell with id %q; list the cells with notebook_read", id)
	}
	if _, ok := args["cell_number"]; !ok {
		if insert {
			return len(cells) - 1, nil
		}
		return 0, fmt.Errorf("cell_id or cell_number is required")
	}
	index := argInt(args, "cell_number", 0, -1, 1<<30)
	if insert && index == -1 {
		return -1, nil
	}
	if index < 0 || index >= len(cells) {
		return 0, fmt.Errorf("cell_number %d is out of range; the notebook has %d cells", index, len(cells))
	}
	return index, nil
}

// shapeCell gives a cell the fields its type requires: a code cell carries
// outputs and an execution count, which a fresh source makes stale; the other
// types carry neither.
func shapeCell(cell map[string]any) {
	if cell["metadata"] == nil {
		cell["metadata"] = map[string]any{}
	}
	if cell["cell_type"] == "code" {
		cell["outputs"] = []any{}
		cell["execution_count"] = nil
		return
	}
	delete(cell, "outputs")
	delete(cell, "execution_count")
}

// notebookWantsIDs reports nbformat 4.5 or later, where every cell has an id.
func notebookWantsIDs(notebook map[string]any) bool {
	major, _ := notebook["nbformat"].(float64)
	minor, _ := notebook["nbformat_minor"].(float64)
	return major > 4 || (major == 4 && minor >= 5)
}

func newCellID() string {
	buffer := make([]byte, 4)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
