package rotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAppendCreatesTheFileWithANewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := Append(path, []byte("first"), Options{}); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "first\n" {
		t.Fatalf("content = %q", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestAppendRotatesAndKeepsArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	opts := Options{MaxBytes: 21, Keep: 2}

	line := func(label string) []byte { return []byte(strings.Repeat(label, 20)) }
	for _, label := range []string{"a", "b", "c"} {
		if err := Append(path, line(label), opts); err != nil {
			t.Fatalf("append %s: %v", label, err)
		}
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live: %v", err)
	}
	if !strings.HasPrefix(string(live), strings.Repeat("c", 20)) {
		t.Fatalf("live = %q", live)
	}
	newest, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read .1: %v", err)
	}
	if !strings.HasPrefix(string(newest), strings.Repeat("b", 20)) {
		t.Fatalf(".1 = %q", newest)
	}
	oldest, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("read .2: %v", err)
	}
	if !strings.HasPrefix(string(oldest), strings.Repeat("a", 20)) {
		t.Fatalf(".2 = %q", oldest)
	}
}

func TestRotationDropsTheOldestArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron.jsonl")
	opts := Options{MaxBytes: 10, Keep: 1}
	for index := 0; index < 4; index++ {
		if err := Append(path, []byte(fmt.Sprintf("line-%d", index)), opts); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("a keep=1 rotation must not leave .2: %v", err)
	}
	archive, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read .1: %v", err)
	}
	if !strings.HasPrefix(string(archive), "line-2") {
		t.Fatalf(".1 = %q", archive)
	}
}

func TestReaderIncludesArchivesOldestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	opts := Options{MaxBytes: 10, Keep: 3}
	for index := 0; index < 3; index++ {
		if err := Append(path, []byte(fmt.Sprintf("entry-%d", index)), opts); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	reader, err := Reader(path)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	text := string(data)
	first := strings.Index(text, "entry-0")
	second := strings.Index(text, "entry-1")
	third := strings.Index(text, "entry-2")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("stream order is wrong: %q", text)
	}
}

func TestReaderOnAMissingFileIsEmpty(t *testing.T) {
	reader, err := Reader(filepath.Join(t.TempDir(), "none.jsonl"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || len(data) != 0 {
		t.Fatalf("data = %q err = %v", data, err)
	}
}

func TestConcurrentAppendsLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(number int) {
			defer group.Done()
			if err := Append(path, []byte(fmt.Sprintf("record-%d", number)), Options{}); err != nil {
				t.Errorf("append: %v", err)
			}
		}(index)
	}
	group.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 32 {
		t.Fatalf("wrote %d complete lines, want 32", got)
	}
}
