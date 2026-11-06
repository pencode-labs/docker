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
	publisher      uiPublisher
	ctx            context.Context
	run            func(context.Context, ...string) (string, error)
	listCancel     context.CancelFunc
	logCancel      context.CancelFunc
	selected       container
	logs           string
	listError      string
	logError       string
	operationError string
	busyID         string
	busyAction     string
	containers     []container
	listContext    peb.ActionContext
	logContext     peb.ActionContext
	listGeneration uint64
	logGeneration  uint64
	mu             sync.Mutex
	listOpen       bool
	listLoading    bool
	logsOpen       bool
	logsVisible    bool
	logLoading     bool
	paused         bool
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
			m.loadList(false)
			m.loadLogs(false)
			m.mu.Unlock()
		}
	}
}

func (m *manager) open(c peb.ActionContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listOpen, m.listContext = true, c
	m.loadList(true)
	return m.showList()
}

// All state and publication below is protected by mu. Docker I/O always runs
// outside the lock, so handlers acknowledge within the host's five-second limit.
func (m *manager) loadList(manual bool) {
	if !m.listOpen || m.listLoading {
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
		if generation != m.listGeneration || !m.listOpen {
			return
		}
		m.listLoading = false
		message := errorText(err)
		changed := manual || message != m.listError || !slices.Equal(containers, m.containers)
		m.listError = message
		if err == nil {
			m.containers = containers
		}
		if changed {
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
		changed := manual || message != m.logError || output != m.logs
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

func (m *manager) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
	}
	m.logGeneration++
	m.logLoading = false
}

func (m *manager) event(e peb.UIEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch e.ID {
	case "containers":
		if e.Event.Kind == "close" {
			m.listOpen = false
			m.stopList()
			return nil
		}
		if !m.listOpen || e.Event.Kind != "click" {
			return nil
		}
		if e.Event.Ref == "refresh" {
			m.loadList(true)
			return m.showList()
		}
		action, id, ok := strings.Cut(e.Event.Ref, ":")
		if !ok {
			return nil
		}
		for _, c := range m.containers {
			if c.ID != id {
				continue
			}
			if action == "logs" {
				m.stopLogs()
				m.selected, m.logContext = c, e.Context
				m.logs, m.logError = "", ""
				m.logsOpen, m.logsVisible, m.paused = true, true, false
				m.loadLogs(true)
				if err := m.showLogs(); err != nil {
					return err
				}
				m.listOpen = false
				m.stopList()
				return m.publisher.CloseUI("containers")
			}
			if m.busyID == "" && c.allows(action) {
				m.operate(action, id)
				return m.showList()
			}
			break
		}
	case "logs":
		switch e.Event.Kind {
		case "close":
			m.logsOpen, m.logsVisible = false, false
			m.stopLogs()
		case "visibilitychange":
			m.logsVisible = e.Event.Value == "visible"
			if !m.logsVisible {
				m.stopLogs()
			} else if !m.paused {
				m.loadLogs(true)
			}
		case "click":
			if !m.logsOpen {
				return nil
			}
			switch e.Event.Ref {
			case "refresh-logs":
				m.loadLogs(true)
			case "pause":
				m.paused = !m.paused
				if m.paused {
					m.stopLogs()
				} else {
					m.loadLogs(true)
				}
			case "back":
				m.listContext, m.listOpen = e.Context, true
				m.loadList(true)
				return m.showList()
			}
			return m.showLogs()
		}
	}
	return nil
}

func (m *manager) operate(action, id string) {
	m.busyID, m.busyAction, m.operationError = id, action, ""
	go func() {
		_, err := m.run(m.ctx, action, "--", id)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.busyID, m.busyAction = "", ""
		m.operationError = errorText(err)
		// Discard a pre-operation snapshot before fetching the resulting state.
		m.stopList()
		m.loadList(true)
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
