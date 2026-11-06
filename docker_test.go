package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testList = `{"ID":"` + testID + `","Names":"web","Image":"nginx:latest","State":"running","Status":"Up 2 minutes","Ports":"8080->80/tcp"}`

// Run the test executable as a fake Docker CLI, exercising real process pipes,
// exit errors and cancellation on every supported OS without a Docker daemon.
func TestMain(m *testing.M) {
	if os.Getenv("PENCODE_DOCKER_HELPER") == "1" {
		switch os.Args[1] {
		case "ps":
			fmt.Fprintln(os.Stdout, testList)
		case "logs":
			fmt.Fprintln(os.Stdout, "application stdout")
			fmt.Fprintln(os.Stderr, "application stderr")
		case "fail":
			fmt.Fprintln(os.Stderr, "permission denied")
			os.Exit(1)
		case "wait":
			time.Sleep(time.Minute)
		case "large":
			fmt.Fprint(os.Stdout, strings.Repeat("x", maxOutput+10))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeDocker(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	name := "docker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("PENCODE_DOCKER_HELPER", "1")
}

func TestRunDocker(t *testing.T) {
	fakeDocker(t)
	output, err := runDocker(t.Context(), "ps", "--all")
	if err != nil || strings.TrimSpace(output) != testList {
		t.Fatalf("list: %q, %v", output, err)
	}
	output, err = runDocker(t.Context(), "logs", "--tail", "300", "--", testID)
	if err != nil || !strings.Contains(output, "application stdout") || !strings.Contains(output, "application stderr") {
		t.Fatalf("both log streams: %q, %v", output, err)
	}
	if _, err := runDocker(t.Context(), "fail"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("stderr diagnostic: %v", err)
	}
	if _, err := runDocker(t.Context(), "large"); err == nil || !strings.Contains(err.Error(), "256 KiB") {
		t.Fatalf("truncated list must fail: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	if _, err := runDocker(ctx, "wait"); err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestDockerMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := runDocker(t.Context(), "ps"); err == nil || !strings.Contains(err.Error(), "未找到 docker") {
		t.Fatalf("missing executable: %v", err)
	}
}

func TestDecodeContainers(t *testing.T) {
	output := strings.ReplaceAll(testList, `"web"`, `"z-web"`) + "\n" + strings.ReplaceAll(testList, `"web"`, `"a-web"`)
	containers, err := decodeContainers(output)
	if err != nil || len(containers) != 2 || containers[0].Names != "a-web" {
		t.Fatalf("sorted JSON lines: %+v, %v", containers, err)
	}
	for _, bad := range []string{"not JSON", testList + "\n{", strings.ReplaceAll(testList, testID, "--help")} {
		if _, err := decodeContainers(bad); err == nil {
			t.Fatalf("accepted malformed output: %q", bad)
		}
	}
	if containers, err := decodeContainers(""); err != nil || len(containers) != 0 {
		t.Fatalf("empty daemon: %+v, %v", containers, err)
	}
}

func TestTailBuffer(t *testing.T) {
	var b tailBuffer
	_, _ = b.Write([]byte(strings.Repeat("a", maxOutput-5)))
	_, _ = b.Write([]byte("1234567890"))
	if !b.truncated || len(b.data) != maxOutput || !strings.HasSuffix(b.text(), "1234567890") {
		t.Fatal("buffer must retain the most recent output")
	}
	_, _ = b.Write([]byte(strings.Repeat("b", maxOutput+20)))
	if b.text() != strings.Repeat("b", maxOutput) {
		t.Fatal("oversized write must replace the tail")
	}
	_, _ = b.Write([]byte{0xff})
	if !strings.HasSuffix(b.text(), "�") {
		t.Fatal("invalid UTF-8 must not break the extension protocol")
	}
	var concurrent tailBuffer
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for range 100 {
				_, _ = concurrent.Write([]byte("stdout or stderr\n"))
			}
		})
	}
	workers.Wait()
	if strings.Count(concurrent.text(), "\n") != 200 {
		t.Fatal("concurrent log streams lost output")
	}
}
