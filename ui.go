package main

import (
	_ "embed"
	"fmt"
	"html"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

//go:embed resources/docker.css
var themeCSS string

var actionLabels = map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}
var actionIcons = map[string]string{"start": "debug-start", "stop": "debug-stop", "restart": "refresh-cw"}

func mappedPorts(ports string) string {
	allMapped := true
	for port := range strings.SplitSeq(ports, ",") {
		if !strings.Contains(port, "->") {
			allMapped = false
			break
		}
	}
	if allMapped {
		return ports
	}
	var b strings.Builder
	for port := range strings.SplitSeq(ports, ",") {
		if strings.Contains(port, "->") {
			if b.Len() != 0 {
				b.WriteString(", ")
			}
			b.WriteString(strings.TrimSpace(port))
		}
	}
	return b.String()
}

func iconButton(b *strings.Builder, ref, icon, label string, disabled bool) {
	fmt.Fprintf(b, `<button class="docker-icon-button" data-ref="%s" type="button" title="%s" aria-label="%s"`, ref, label, label)
	if disabled {
		b.WriteString(` disabled`)
	}
	fmt.Fprintf(b, `><img data-ref="%s-icon" data-pencode-icon="%s" alt=""></button>`, ref, icon)
}

func errorView(message string) *peb.EditorView {
	return &peb.EditorView{HTML: `<p class="docker-error" role="alert">` + html.EscapeString(message) + `</p>`, CSS: themeCSS}
}

func (m *manager) showList() error {
	var b strings.Builder
	b.Grow(512 + len(m.containers)*1000)
	b.WriteString(`<section class="docker-manager"><header><div><strong>容器</strong><span class="docker-muted"> · 当前 Docker context</span></div>`)
	iconButton(&b, "refresh", "refresh-cw", "刷新容器列表", m.listLoading)
	b.WriteString(`</header><div class="docker-notice" data-ref="notice" data-pencode-region>`)
	for _, message := range []string{m.operationError, m.listError} {
		if message != "" {
			b.WriteString(`<p class="docker-error" role="alert">` + html.EscapeString(message) + `</p>`)
		}
	}
	if m.busyID != "" {
		b.WriteString(`<p class="docker-muted" role="status">正在` + actionLabels[m.busyAction] + `容器…</p>`)
	}
	b.WriteString(`</div><div class="docker-table" role="table" aria-label="Docker 容器"><div class="docker-row docker-table-head" role="row"><span role="columnheader" aria-label="状态"></span><span role="columnheader">名称</span><span role="columnheader">端口</span><span role="columnheader">运行状态</span><span role="columnheader">操作</span></div><div class="docker-containers" role="rowgroup" data-ref="container-list" data-pencode-scroll="preserve" data-pencode-region>`)
	events := make([]peb.EditorBinding, 1, 1+len(m.containers)*4)
	events[0] = peb.EditorBinding{Ref: "refresh", Kind: "click"}
	if len(m.containers) == 0 {
		message := "当前没有容器。"
		if m.listLoading {
			message = "正在读取容器列表…"
		} else if m.listError != "" {
			message = "无法连接 Docker。请检查 Docker 服务、当前 context 和访问权限，然后重试。"
		}
		b.WriteString(`<p class="docker-empty">` + message + `</p>`)
	}
	for _, c := range m.containers {
		state := html.EscapeString(c.State)
		name := html.EscapeString(c.Names)
		ports, status := html.EscapeString(mappedPorts(c.Ports)), html.EscapeString(c.Status)
		if ports == "" {
			ports = "—"
		}
		fmt.Fprintf(&b, `<div class="docker-row docker-container" role="row"><span class="docker-state docker-state-%s" role="cell" title="%s" aria-label="%s"><span class="docker-state-dot" aria-hidden="true"></span></span><div role="cell"><button class="docker-name" data-ref="logs:%s" type="button" title="查看日志 · %s">%s</button></div><span class="docker-ports" role="cell" title="%s">%s</span><span class="docker-status" role="cell" title="%s">%s</span><div class="docker-actions" role="cell">`, state, state, state, c.ID, name, name, ports, ports, status, status)
		events = append(events, peb.EditorBinding{Ref: "logs:" + c.ID, Kind: "click"})
		for _, action := range [...]string{"start", "stop", "restart"} {
			ref := action + ":" + c.ID
			iconButton(&b, ref, actionIcons[action], actionLabels[action]+"容器", m.busyID != "" || !c.allows(action))
			events = append(events, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</div></div><footer class="docker-muted">点击名称查看日志 · 每 3 秒自动刷新 · `)
	fmt.Fprintf(&b, `%d 个容器</footer></section>`, len(m.containers))
	return m.publisher.Show(peb.UI{ID: "containers", Title: "Docker 容器", Kind: peb.Popup, Anchor: "containers", Context: m.listContext,
		CloseOnOutsideClick: true, Placement: peb.UIPlacement{Mode: peb.PositionAnchor, X: -700, Y: 8, Width: 740, Height: 540},
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: events}})
}

func (m *manager) showLogs() error {
	var b strings.Builder
	b.Grow(len(m.logs) + 768)
	label, icon := "暂停自动刷新", "debug-pause"
	if m.paused {
		label, icon = "恢复自动刷新", "debug-start"
	}
	b.WriteString(`<section class="docker-logs" data-pencode-layout="fill"><header class="docker-log-header"><strong class="docker-log-title">Docker 日志 · ` + html.EscapeString(m.selected.Names) + `</strong><span class="docker-log-hint docker-muted">最近 300 行 · 含时间戳</span><div class="docker-actions" role="group" aria-label="日志操作">`)
	iconButton(&b, "back", "arrow-right", "容器列表", false)
	iconButton(&b, "pause", icon, label, false)
	iconButton(&b, "refresh-logs", "refresh-cw", "刷新日志", false)
	b.WriteString(`</div></header><div class="docker-log-scroll" data-ref="log-scroll" data-pencode-scroll="end" data-pencode-scroll-key="` + m.selected.ID + `" data-pencode-selectable><div data-ref="log-notice" data-pencode-region>`)
	if m.logError != "" {
		b.WriteString(`<p class="docker-error" role="alert">` + html.EscapeString(m.logError) + `</p>`)
	}
	if m.logs == "" {
		message := "暂无日志。"
		if m.logLoading {
			message = "正在读取日志…"
		}
		b.WriteString(`<p class="docker-muted" role="status">` + message + `</p>`)
	}
	b.WriteString(`</div><pre data-ref="log-output" data-pencode-region>`)
	b.WriteString(html.EscapeString(m.logs))
	b.WriteString(`</pre></div></section>`)
	return m.publisher.Show(peb.UI{ID: "logs", Title: "Docker 日志 · " + m.selected.Names, Kind: peb.Panel, Context: m.logContext,
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: []peb.EditorBinding{{Ref: "back", Kind: "click"}, {Ref: "pause", Kind: "click"}, {Ref: "refresh-logs", Kind: "click"}}}})
}
