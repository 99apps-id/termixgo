package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionScheduleFiresAsAWake(t *testing.T) {
	scheduler := NewSessionScheduler()
	var mu sync.Mutex
	var wakes []Event
	scheduler.SetEmitter(func(event Event) {
		mu.Lock()
		wakes = append(wakes, event)
		mu.Unlock()
	})
	t.Cleanup(scheduler.StopAll)
	env := testEnv(t)
	env.Schedules = scheduler

	at := time.Now().Add(1100 * time.Millisecond).Format("2006-01-02 15:04:05")
	result, _ := (&scheduleTaskTool{}).Run(context.Background(), env, map[string]any{"schedule": "@at " + at, "prompt": "check the deploy"})
	if result.IsError {
		t.Fatalf("schedule: %s", result.Output)
	}
	if !strings.Contains(scheduler.List(), "check the deploy") {
		t.Fatalf("list = %q", scheduler.List())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(wakes)
		mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wakes) != 1 || wakes[0].Kind != EventWake || !strings.Contains(wakes[0].Text, "check the deploy") {
		t.Fatalf("wakes = %+v", wakes)
	}
	if !strings.Contains(scheduler.List(), "No scheduled tasks") {
		t.Fatalf("a one-shot is gone after it fires: %q", scheduler.List())
	}
}

func TestScheduleRejectsFastRecurrenceAndDeletes(t *testing.T) {
	scheduler := NewSessionScheduler()
	scheduler.SetEmitter(func(Event) {})
	t.Cleanup(scheduler.StopAll)
	env := testEnv(t)
	env.Schedules = scheduler
	fast, _ := (&scheduleTaskTool{}).Run(context.Background(), env, map[string]any{"schedule": "@every 10s", "prompt": "x"})
	if !fast.IsError {
		t.Fatalf("a ten-second loop must be refused: %+v", fast)
	}
	ok, _ := (&scheduleTaskTool{}).Run(context.Background(), env, map[string]any{"schedule": "@every 30m", "prompt": "x"})
	if ok.IsError {
		t.Fatalf("create = %+v", ok)
	}
	removed, _ := (&scheduleTaskTool{}).Run(context.Background(), env, map[string]any{"action": "delete", "id": "task-1"})
	if removed.IsError {
		t.Fatalf("delete = %+v", removed)
	}
	durable, _ := (&scheduleTaskTool{}).Run(context.Background(), env, map[string]any{"schedule": "@every 1h", "prompt": "x", "durable": true})
	if !durable.IsError {
		t.Fatalf("durable without a cron store must fail")
	}
}

const sampleNotebook = `{
 "cells": [
  {"cell_type": "markdown", "id": "aa11", "metadata": {}, "source": ["# Title\n"]},
  {"cell_type": "code", "execution_count": 3, "id": "bb22", "metadata": {"tags": ["x"]},
   "outputs": [{"output_type": "stream", "name": "stdout", "text": ["hello\n"]}],
   "source": ["print('hello')"]}
 ],
 "metadata": {"kernelspec": {"name": "python3"}},
 "nbformat": 4,
 "nbformat_minor": 5
}`

func TestNotebookReadAndEdit(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "nb.ipynb")
	if err := os.WriteFile(path, []byte(sampleNotebook), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _ := (&notebookReadTool{}).Run(context.Background(), env, map[string]any{"path": "nb.ipynb"})
	for _, want := range []string{"cell 0 [markdown] id=aa11", "print('hello')", "output:\nhello"} {
		if !strings.Contains(read.Output, want) {
			t.Fatalf("read lacks %q:\n%s", want, read.Output)
		}
	}

	edit := &notebookEditTool{}
	if r, _ := edit.Run(context.Background(), env, map[string]any{"path": "nb.ipynb", "cell_id": "bb22", "new_source": "x = 1\nprint(x)"}); r.IsError {
		t.Fatalf("replace: %s", r.Output)
	}
	if r, _ := edit.Run(context.Background(), env, map[string]any{"path": "nb.ipynb", "cell_number": -1, "edit_mode": "insert", "cell_type": "code", "new_source": "import os"}); r.IsError {
		t.Fatalf("insert: %s", r.Output)
	}
	if r, _ := edit.Run(context.Background(), env, map[string]any{"path": "nb.ipynb", "cell_id": "aa11", "edit_mode": "delete"}); r.IsError {
		t.Fatalf("delete: %s", r.Output)
	}

	data, _ := os.ReadFile(path)
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		t.Fatalf("the notebook must stay valid JSON: %v", err)
	}
	cells := notebook["cells"].([]any)
	if len(cells) != 2 {
		t.Fatalf("cells = %d", len(cells))
	}
	inserted := cells[0].(map[string]any)
	if notebookText(inserted["source"]) != "import os" || inserted["id"] == nil || inserted["outputs"] == nil {
		t.Fatalf("inserted = %+v", inserted)
	}
	replaced := cells[1].(map[string]any)
	if notebookText(replaced["source"]) != "x = 1\nprint(x)" || len(replaced["outputs"].([]any)) != 0 || replaced["execution_count"] != nil {
		t.Fatalf("replaced = %+v", replaced)
	}
	if replaced["metadata"].(map[string]any)["tags"] == nil || notebook["metadata"].(map[string]any)["kernelspec"] == nil {
		t.Fatalf("unknown fields must survive an edit")
	}
}

func TestAskUserTakesSeveralQuestions(t *testing.T) {
	env := testEnv(t)
	var asked []string
	env.Ask = func(question string, options []string) (string, error) {
		asked = append(asked, question)
		return options[0], nil
	}
	result, _ := (&askUserTool{}).Run(context.Background(), env, map[string]any{"questions": []any{
		map[string]any{"question": "Which database?", "options": []any{"Postgres", "SQLite"}},
		map[string]any{"question": "Add tests?", "options": []any{"Yes", "No"}},
	}})
	if result.IsError || len(asked) != 2 || asked[0] != "(1/2) Which database?" {
		t.Fatalf("asked %q, result %+v", asked, result)
	}
	if !strings.Contains(result.Output, "1. Which database?\n   Answer: Postgres") || !strings.Contains(result.Output, "2. Add tests?\n   Answer: Yes") {
		t.Fatalf("output = %q", result.Output)
	}
}
