package base

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"opencodebot/internal/docker"
)

func TestExecRunsInWorkdirWithEnvironment(t *testing.T) {
	container := New()
	dir := t.TempDir()
	result, err := container.Exec(context.Background(), docker.ExecOptions{
		Cmd:     []string{"/bin/bash", "-c", "printf '%s %s' \"$GREETING\" \"$(pwd)\""},
		Workdir: dir,
		Env:     map[string]string{"GREETING": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if got, want := string(result.Output), "hello "+dir; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPutAndGetArchiveRoundTrip(t *testing.T) {
	container := New()
	dir := t.TempDir()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	payload := []byte("hello")
	if err := writer.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := container.PutArchive(context.Background(), dir, &archive); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}

	reader, err := container.GetArchive(context.Background(), filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "note.txt" {
		t.Fatalf("header name = %q", header.Name)
	}
}

func TestPutArchiveRejectsTraversal(t *testing.T) {
	container := New()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("x"))
	writer.Close()
	if err := container.PutArchive(context.Background(), t.TempDir(), &archive); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
