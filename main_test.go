package main

import (
	"context"
	"encoding/json"
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
		return ui.View != nil && strings.Contains(ui.View.HTML, `data-ref="logs:`+testID+`"`)
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
	if ui.Kind != peb.Panel || strings.Contains(ui.View.HTML, "<script>") || !strings.Contains(ui.View.HTML, `data-pencode-terminal-output="`) || !publisher.latest("containers").Close {
		t.Fatalf("escaped logs in following panel: %+v", ui)
	}
	if strings.Contains(ui.View.HTML, `data-ref="back"`) || !strings.Contains(ui.View.HTML, `data-pencode-icon="clear-all"`) ||
		!strings.Contains(ui.View.HTML, `<option selected="" value="`+testID+`">web</option>`) ||
		!slices.Contains(ui.View.Events, peb.EditorBinding{Ref: "log-container", Kind: "change"}) {
		t.Fatal("log panel must show the selected container and clear action")
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
	if err := app.event(click("logs", "clear-logs")); err != nil {
		t.Fatal(err)
	}
	if ui := publisher.latest("logs"); strings.Contains(ui.View.HTML, "&lt;script&gt;") || !strings.Contains(ui.View.HTML, "暂无日志") || !strings.Contains(ui.View.HTML, `data-pencode-icon="clear-all"`) {
		t.Fatal("clear logs did not clear the visible output")
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
	if strings.Contains(publisher.latest("containers").View.HTML, `data-ref="logs:`+testID+`"`) {
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
	change := peb.UIEvent{ID: "logs", Event: peb.EditorEvent{Kind: "change", Ref: "log-container"}}
	for _, id := range []string{"unknown", testID, other.ID} {
		change.Event.Value = id
		if err := app.event(change); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		return strings.Contains(publisher.latest("logs").View.HTML, "second container log")
	})
	ui := publisher.latest("logs")
	if strings.Contains(ui.View.HTML, "stale log") || !strings.Contains(ui.View.HTML, `<option selected="" value="`+other.ID+`">second</option>`) ||
		!strings.Contains(ui.View.HTML, `data-pencode-terminal-output="`+other.ID+`"`) || !publisher.latest("containers").Close {
		t.Fatal("switching in the panel must select the new stream and keep the popup closed")
	}
}

func TestPausedLogPanelRefreshesContainerChoices(t *testing.T) {
	publisher := &recordingPublisher{}
	const newID = "abcdef012345"
	app := manager{ctx: t.Context(), publisher: publisher, listOpen: true, containers: []container{{ID: testID, Names: "web"}}, run: func(_ context.Context, args ...string) (string, error) {
		if args[0] == "ps" {
			return strings.NewReplacer(testID, newID, `"web"`, `"worker"`).Replace(testList), nil
		}
		return "output from " + args[len(args)-1], nil
	}}
	if err := app.event(click("containers", "logs:"+testID)); err != nil {
		t.Fatal(err)
	}
	if err := app.event(click("logs", "pause")); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.loadList()
	app.mu.Unlock()
	waitFor(t, func() bool {
		return strings.Contains(publisher.latest("logs").View.HTML, `<option value="`+newID+`">worker</option>`)
	})
	if !strings.Contains(publisher.latest("logs").View.HTML, "web（已移除）") {
		t.Fatal("a removed container's logs were relabeled")
	}
	if err := app.event(peb.UIEvent{ID: "logs", Event: peb.EditorEvent{Kind: "change", Ref: "log-container", Value: newID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(publisher.latest("logs").View.HTML, "output from "+newID) })
	if !strings.Contains(publisher.latest("logs").View.HTML, "恢复自动刷新") {
		t.Fatal("switching containers must preserve the user's pause setting")
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

func TestCollapsedGroupOperations(t *testing.T) {
	const prefix = "xiuxing-dev-"
	containers := []container{
		{ID: "111111111111", Names: prefix + "mysql-1", State: "running"},
		{ID: "222222222222", Names: prefix + "php-1", State: "exited"},
		{ID: "333333333333", Names: prefix + "redis-1", State: "running"},
		{ID: "444444444444", Names: "xiuxing2-dev-other", State: "running"},
	}
	for _, tc := range []struct {
		action string
		icon   string
		ids    []string
	}{
		{"start", "debug-start", []string{containers[1].ID}}, {"stop", "debug-stop", []string{containers[0].ID, containers[2].ID}},
		{"restart", "refresh-cw", []string{containers[0].ID, containers[1].ID, containers[2].ID}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			publisher := &recordingPublisher{}
			calls := make(chan []string, 4)
			var snapshot strings.Builder
			encoder := json.NewEncoder(&snapshot)
			for _, c := range containers {
				if err := encoder.Encode(c); err != nil {
					t.Fatal(err)
				}
			}
			app := manager{ctx: t.Context(), publisher: publisher, listOpen: true, containers: containers, run: func(_ context.Context, args ...string) (string, error) {
				calls <- args
				if args[0] == "ps" {
					return snapshot.String(), nil
				}
				return "", nil
			}}
			press := func(ref string) {
				t.Helper()
				if err := app.event(click("containers", ref)); err != nil {
					t.Fatal(err)
				}
			}
			press("toggle:group:" + prefix)
			collapsed := func() bool {
				h := publisher.latest("containers").View.HTML
				return strings.Contains(h, `aria-expanded="false"`) && !strings.Contains(h, `data-ref="logs:`+containers[0].ID+`"`) && strings.Contains(h, `data-ref="logs:`+containers[3].ID+`"`)
			}
			if !collapsed() {
				t.Fatal("collapse must hide only the group's members")
			}
			view := publisher.latest("containers").View
			for _, target := range []string{"group:" + prefix, containers[3].ID} {
				ref := tc.action + ":" + target
				if !strings.Contains(view.HTML, `<img alt="" data-pencode-icon="`+tc.icon+`" data-ref="`+ref+`-icon"></button>`) ||
					!slices.Contains(view.Events, peb.EditorBinding{Ref: ref, Kind: "click"}) {
					t.Fatalf("%s is missing its icon or click binding", ref)
				}
			}
			press(tc.action + ":group:xiuxing-")
			press(tc.action + ":group:" + prefix)
			if args := <-calls; !slices.Equal(args, append([]string{tc.action, "--"}, tc.ids...)) {
				t.Fatalf("unexpected group operation %q", args)
			}
			if args := <-calls; args[0] != "ps" {
				t.Fatalf("expected post-operation refresh, got %q", args)
			}
			waitFor(t, func() bool { app.mu.Lock(); defer app.mu.Unlock(); return !app.listLoading && app.busyAction == "" })
			if !collapsed() {
				t.Fatal("refresh lost collapsed state")
			}
			press("toggle:group:" + prefix)
			if !strings.Contains(publisher.latest("containers").View.HTML, `data-ref="logs:`+containers[1].ID+`"`) {
				t.Fatal("expanded group must restore individual controls")
			}
		})
	}
}
