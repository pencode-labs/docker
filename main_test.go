package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

type recordingPublisher struct {
	updates []peb.UI
	mu      sync.Mutex
}

func (p *recordingPublisher) Show(ui peb.UI) error {
	// Validate the actual wire limits, including UTF-8 and event bindings.
	if _, err := peb.EncodeUI(nil, ui); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, ui)
	return nil
}

func (p *recordingPublisher) CloseUI(id string) error { return p.Show(peb.UI{ID: id, Close: true}) }

func (p *recordingPublisher) latest(id string) peb.UI {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ui := range slices.Backward(p.updates) {
		if ui.ID == id {
			return ui
		}
	}
	return peb.UI{}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for background Docker operation")
		case <-ticker.C:
		}
	}
}

func click(id, ref string) peb.UIEvent {
	return peb.UIEvent{ID: id, Context: peb.ActionContext{ID: "containers", ProjectPath: "/workspace"}, Event: peb.EditorEvent{Kind: "click", Ref: ref}}
}

func TestContainerLifecycleAndLogs(t *testing.T) {
	publisher := &recordingPublisher{}
	calls := make(chan []string, 16)
	app := manager{ctx: t.Context(), publisher: publisher, run: func(_ context.Context, args ...string) (string, error) {
		calls <- args
		switch args[0] {
		case "ps":
			return testList, nil
		case "logs":
			return "2026-11-06T00:00:00Z <script>alert('log')</script>\n", nil
		}
		return "", nil
	}}
	if err := app.open(peb.ActionContext{ID: "containers", ProjectPath: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ui := publisher.latest("containers")
		return ui.View != nil && strings.Contains(ui.View.HTML, "nginx:latest")
	})
	ui := publisher.latest("containers")
	if ui.Kind != peb.Popup || ui.Context.ProjectPath != "/workspace" || ui.Anchor != "containers" {
		t.Fatalf("container popup: %+v", ui)
	}
	if args := <-calls; !slices.Equal(args, []string{"ps", "--all", "--no-trunc", "--format", "{{json .}}"}) {
		t.Fatalf("list args: %q", args)
	}
	if err := app.event(click("containers", "stop:"+testID)); err != nil {
		t.Fatal(err)
	}
	if args := <-calls; !slices.Equal(args, []string{"stop", "--", testID}) {
		t.Fatalf("stop args: %q", args)
	}
	if args := <-calls; args[0] != "ps" {
		t.Fatalf("operation must reload state: %q", args)
	}
	waitFor(t, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return !app.listLoading
	})
	if err := app.event(click("containers", "logs:"+testID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ui := publisher.latest("logs")
		return ui.View != nil && strings.Contains(ui.View.HTML, "&lt;script&gt;")
	})
	ui = publisher.latest("logs")
	if ui.Kind != peb.Panel || strings.Contains(ui.View.HTML, "<script>") || !strings.Contains(ui.View.HTML, `data-pencode-scroll="end"`) || !publisher.latest("containers").Close {
		t.Fatalf("escaped logs in following panel: %+v", ui)
	}
	if args := <-calls; !slices.Equal(args, []string{"logs", "--tail", "300", "--timestamps", "--", testID}) {
		t.Fatalf("log args: %q", args)
	}
	if err := app.event(click("logs", "pause")); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.loadLogs(false)
	paused := app.paused
	loading := app.logLoading
	app.mu.Unlock()
	if !paused || loading || !strings.Contains(publisher.latest("logs").View.HTML, "恢复自动刷新") {
		t.Fatal("pause state was not rendered")
	}
}

func TestClosingCancelsPendingList(t *testing.T) {
	publisher := &recordingPublisher{}
	started, finished := make(chan struct{}), make(chan struct{})
	app := manager{ctx: t.Context(), publisher: publisher, run: func(ctx context.Context, _ ...string) (string, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return testList, nil // A late response must not reopen the popup.
	}}
	if err := app.open(peb.ActionContext{ID: "containers"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := app.event(peb.UIEvent{ID: "containers", Event: peb.EditorEvent{Kind: "close"}}); err != nil {
		t.Fatal(err)
	}
	<-finished
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.listOpen || app.listLoading || app.listGeneration != 1 {
		t.Fatal("closing must cancel and invalidate the request")
	}
	if strings.Contains(publisher.latest("containers").View.HTML, "nginx:latest") {
		t.Fatal("late list response was applied")
	}
}

func TestSlowOperationRemainsResponsive(t *testing.T) {
	publisher := &recordingPublisher{}
	started, canceled := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	containers, err := decodeContainers(testList)
	if err != nil {
		t.Fatal(err)
	}
	app := manager{ctx: ctx, publisher: publisher, listOpen: true, containers: containers, run: func(ctx context.Context, args ...string) (string, error) {
		if args[0] != "stop" {
			t.Errorf("unexpected operation: %q", args)
			return "", nil
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}}
	if err := app.event(click("containers", "stop:"+testID)); err != nil {
		t.Fatal(err)
	}
	<-started
	if !strings.Contains(publisher.latest("containers").View.HTML, "正在停止") {
		t.Fatal("busy state must be shown before the operation completes")
	}
	if err := app.event(click("containers", "restart:"+testID)); err != nil {
		t.Fatal(err)
	}
	if err := app.event(peb.UIEvent{ID: "containers", Event: peb.EditorEvent{Kind: "close"}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-canceled
}

func TestLogSwitchDiscardsOldResponse(t *testing.T) {
	publisher := &recordingPublisher{}
	containers, err := decodeContainers(testList)
	if err != nil {
		t.Fatal(err)
	}
	other := containers[0]
	other.ID, other.Names = strings.Repeat("f", 64), "second"
	containers = append(containers, other)
	started := make(chan struct{})
	app := manager{ctx: t.Context(), publisher: publisher, listOpen: true, containers: containers, run: func(ctx context.Context, args ...string) (string, error) {
		if args[len(args)-1] == testID {
			close(started)
			<-ctx.Done()
			return "stale log", nil
		}
		return "second container log", nil
	}}
	if err := app.event(click("containers", "logs:"+testID)); err != nil {
		t.Fatal(err)
	}
	<-started
	app.mu.Lock()
	app.listOpen = true
	app.mu.Unlock()
	if err := app.event(click("containers", "logs:"+other.ID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return strings.Contains(publisher.latest("logs").View.HTML, "second container log")
	})
	if strings.Contains(publisher.latest("logs").View.HTML, "stale log") {
		t.Fatal("previous container's output replaced the new selection")
	}
}

func TestFailedOperationAndUntrustedEvents(t *testing.T) {
	publisher := &recordingPublisher{}
	containers, err := decodeContainers(testList)
	if err != nil {
		t.Fatal(err)
	}
	app := manager{ctx: t.Context(), publisher: publisher, listOpen: true, containers: containers, run: func(_ context.Context, args ...string) (string, error) {
		if args[0] == "ps" {
			return testList, nil
		}
		if args[0] != "restart" || args[len(args)-1] != testID {
			t.Errorf("unexpected operation: %q", args)
		}
		return "", errors.New("daemon unavailable <retry>")
	}}
	for _, ref := range []string{"stop:--help", "remove:" + testID, "start:" + testID, "stop:unknown"} {
		if err := app.event(click("containers", ref)); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.event(click("containers", "restart:"+testID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ui := publisher.latest("containers")
		return ui.View != nil && strings.Contains(ui.View.HTML, "daemon unavailable &lt;retry&gt;")
	})
}
