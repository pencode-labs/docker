package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

type uiPublisher interface {
	Show(peb.UI) error
	CloseUI(string) error
}

type manager struct {
	publisher        uiPublisher
	ctx              context.Context
	run              func(context.Context, ...string) (string, error)
	listCancel       context.CancelFunc
	logCancel        context.CancelFunc
	selected         container
	logOptions       string
	logs             string
	listError        string
	logError         string
	operationError   string
	collapsed        map[string]bool
	busyAction       string
	containers       []container
	listContext      peb.ActionContext
	logContext       peb.ActionContext
	listGeneration   uint64
	logGeneration    uint64
	mu               sync.Mutex
	listOpen         bool
	listLoading      bool
	listLoadingShown bool
	logsOpen         bool
	logsVisible      bool
	logLoading       bool
	logLoadingShown  bool
	paused           bool
}

func main() {
	app := manager{run: runDocker}
	err := peb.ServeExtension(os.Stdin, os.Stdout, peb.Contributions{
		Actions: []peb.Action{{ID: "containers", Tooltip: "Docker 容器", Icon: "resources/container.svg", Location: peb.TitleBarRight}},
	}, peb.Handlers{
		ActivateActions: func(ctx context.Context, p *peb.ActionPublisher) {
			app.ctx, app.publisher = ctx, p
			go app.poll()
		},
		Action:  app.open,
		UIEvent: app.event,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (m *manager) poll() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			m.loadList()
			m.loadLogs(false)
			m.mu.Unlock()
		}
	}
}

func (m *manager) open(c peb.ActionContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listOpen, m.listContext = true, c
	m.loadList()
	return m.showList()
}

// All state and publication below is protected by mu. Docker I/O always runs
// outside the lock, so handlers acknowledge within the host's five-second limit.
func (m *manager) loadList() {
	if (!m.listOpen && !m.logsVisible) || m.listLoading {
		return
	}
	m.listLoading = true
	ctx, cancel := context.WithCancel(m.ctx)
	m.listCancel = cancel
	generation := m.listGeneration
	go func() {
		defer cancel()
		output, err := m.run(ctx, "ps", "--all", "--no-trunc", "--format", "{{json .}}")
		var containers []container
		if err == nil {
			containers, err = decodeContainers(output)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if generation != m.listGeneration {
			return
		}
		m.listLoading = false
		message := errorText(err)
		changed := err == nil && !slices.Equal(containers, m.containers)
		refreshList := m.listOpen && (m.listLoadingShown || message != m.listError || changed)
		m.listError = message
		if changed {
			m.containers = containers
			if m.logsOpen && m.updateLogOptions() && m.logsVisible {
				m.publish(m.showLogs())
			}
		}
		if refreshList {
			m.publish(m.showList())
		}
	}()
}

func (m *manager) loadLogs(manual bool) {
	if !m.logsOpen || !m.logsVisible || m.logLoading || !manual && m.paused {
		return
	}
	m.logLoading = true
	ctx, cancel := context.WithCancel(m.ctx)
	m.logCancel = cancel
	generation, id := m.logGeneration, m.selected.ID
	go func() {
		defer cancel()
		output, err := m.run(ctx, "logs", "--tail", "300", "--timestamps", "--", id)
		m.mu.Lock()
		defer m.mu.Unlock()
		if generation != m.logGeneration || !m.logsOpen || !m.logsVisible {
			return
		}
		m.logLoading = false
		message := errorText(err)
		changed := manual || m.logLoadingShown || message != m.logError || output != m.logs
		m.logError = message
		if err == nil {
			m.logs = output
		}
		if changed {
			m.publish(m.showLogs())
		}
	}()
}

func (m *manager) stopList() {
	if m.listCancel != nil {
		m.listCancel()
	}
	m.listGeneration++
	m.listLoading = false
}

func (m *manager) stopUnusedList() {
	if !m.listOpen && !m.logsVisible {
		m.stopList()
	}
}

func (m *manager) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
	}
	m.logGeneration++
	m.logLoading = false
}

func (m *manager) selectLogs(c container) error {
	m.stopLogs()
	m.selected = c
	m.logs, m.logError = "", ""
	m.updateLogOptions()
	m.loadLogs(true)
	return m.showLogs()
}

func (m *manager) event(e peb.UIEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch e.ID {
	case "containers":
		if e.Event.Kind == "close" {
			m.listOpen = false
			m.stopUnusedList()
			return nil
		}
		if !m.listOpen || e.Event.Kind != "click" {
			return nil
		}
		if e.Event.Ref == "refresh" {
			m.loadList()
			return m.showList()
		}
		action, id, ok := strings.Cut(e.Event.Ref, ":")
		if !ok {
			return nil
		}
		if group, ok := strings.CutPrefix(id, "group:"); ok {
			for prefix, members := range containerGroups(m.containers) {
				if prefix == "" || prefix != group {
					continue
				}
				if action == "toggle" {
					if m.collapsed == nil {
						m.collapsed = make(map[string]bool)
					}
					if m.collapsed[prefix] {
						delete(m.collapsed, prefix)
					} else {
						m.collapsed[prefix] = true
					}
					return m.showList()
				}
				if m.busyAction != "" {
					return nil
				}
				var ids []string
				for _, c := range members {
					if c.allows(action) {
						ids = append(ids, c.ID)
					}
				}
				if len(ids) > 0 {
					m.operate(action, ids...)
					return m.showList()
				}
				break
			}
			return nil
		}
		for _, c := range m.containers {
			if c.ID != id {
				continue
			}
			if action == "logs" {
				m.logContext = e.Context
				m.logsOpen, m.logsVisible, m.paused = true, true, false
				if err := m.selectLogs(c); err != nil {
					return err
				}
				m.listOpen = false
				return m.publisher.CloseUI("containers")
			}
			if m.busyAction == "" && c.allows(action) {
				m.operate(action, id)
				return m.showList()
			}
			break
		}
	case "logs":
		if !m.logsOpen {
			return nil
		}
		switch e.Event.Kind {
		case "close":
			m.logsOpen, m.logsVisible = false, false
			m.stopLogs()
			m.stopUnusedList()
		case "visibilitychange":
			m.logsVisible = e.Event.Value == "visible"
			if !m.logsVisible {
				m.stopLogs()
				m.stopUnusedList()
			} else {
				m.loadList()
				if !m.paused {
					m.loadLogs(true)
				}
				return m.showLogs()
			}
		case "change":
			if e.Event.Ref == "log-container" && m.logsVisible && e.Event.Value != m.selected.ID {
				for _, c := range m.containers {
					if c.ID == e.Event.Value {
						return m.selectLogs(c)
					}
				}
			}
		case "click":
			switch e.Event.Ref {
			case "clear-logs":
				m.stopLogs()
				m.logs, m.logError = "", ""
			case "refresh-logs":
				m.loadLogs(true)
			case "pause":
				m.paused = !m.paused
				if m.paused {
					m.stopLogs()
				} else {
					m.loadLogs(true)
				}
			default:
				return nil
			}
			return m.showLogs()
		}
	}
	return nil
}

func (m *manager) operate(action string, ids ...string) {
	m.busyAction, m.operationError = action, ""
	go func() {
		_, err := m.run(m.ctx, append([]string{action, "--"}, ids...)...)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.busyAction = ""
		m.operationError = errorText(err)
		// Discard a pre-operation snapshot before fetching the resulting state.
		m.stopList()
		m.loadList()
		if m.listOpen {
			m.publish(m.showList())
		} else if err != nil {
			m.publish(m.publisher.Show(peb.UI{ID: "operation-error", Title: "Docker 操作失败", Kind: peb.Notification,
				Context: m.listContext, Severity: peb.NotificationError,
				View: errorView(m.operationError)}))
		}
		if !m.paused {
			m.loadLogs(true)
		}
	}()
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func (m *manager) publish(err error) {
	if err != nil && m.ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
