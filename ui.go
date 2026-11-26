package main

import (
	_ "embed"
	"slices"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
)

//go:embed resources/docker.css
var themeCSS string

var actionLabels = map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}

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

func iconButton(b *markup.Builder, ref, icon, label string, disabled bool) {
	button := b.Open("button", "class", "docker-icon-button", "data-ref", ref, "type", "button", "title", label, "aria-label", label)
	if disabled {
		button.SetAttr("disabled", "")
	}
	b.Element("img", "data-ref", ref+"-icon", "data-pencode-icon", icon, "alt", "")
	b.End()
}

func errorView(message string) *peb.EditorView {
	b := markup.Acquire()
	defer b.Release()
	b.TextElement("p", message, "class", "docker-error", "role", "alert")
	return &peb.EditorView{HTML: b.String(), CSS: themeCSS}
}

func (m *manager) showList() error {
	m.listLoadingShown = m.listLoading
	b := markup.Acquire()
	defer b.Release()
	b.Open("section", "class", "docker-manager")
	b.Open("header")
	b.Open("div")
	b.TextElement("strong", "容器")
	b.TextElement("span", " · 当前 Docker context", "class", "docker-muted")
	b.End()
	iconButton(b, "refresh", "refresh-cw", "刷新容器列表", m.listLoading)
	b.End()
	b.Open("div", "class", "docker-notice", "data-ref", "notice", "data-pencode-region", "")
	for _, message := range []string{m.operationError, m.listError} {
		if message != "" {
			b.TextElement("p", message, "class", "docker-error", "role", "alert")
		}
	}
	if m.busyAction != "" {
		b.TextElement("p", "正在"+actionLabels[m.busyAction]+"容器…", "class", "docker-muted", "role", "status")
	}
	b.End()
	b.Open("div", "class", "docker-table", "role", "table", "aria-label", "Docker 容器")
	b.Open("div", "class", "docker-row docker-table-head", "role", "row")
	b.Element("span", "role", "columnheader", "aria-label", "状态")
	b.TextElement("span", "名称", "role", "columnheader")
	b.TextElement("span", "端口", "role", "columnheader")
	b.TextElement("span", "运行状态", "role", "columnheader")
	b.TextElement("span", "操作", "role", "columnheader")
	b.End()
	b.Open("div", "class", "docker-containers", "role", "rowgroup", "data-ref", "container-list", "data-pencode-scroll", "preserve", "data-pencode-region", "")
	events := make([]peb.EditorBinding, 1, 1+len(m.containers)*4)
	events[0] = peb.EditorBinding{Ref: "refresh", Kind: "click"}
	if len(m.containers) == 0 {
		message := "当前没有容器。"
		if m.listLoading {
			message = "正在读取容器列表…"
		} else if m.listError != "" {
			message = "无法连接 Docker。请检查 Docker 服务、当前 context 和访问权限，然后重试。"
		}
		b.TextElement("p", message, "class", "docker-empty")
	}
	for prefix, members := range containerGroups(m.containers) {
		if prefix != "" {
			target := "group:" + prefix
			ref := "toggle:" + target
			icon := "chevron-down"
			if m.collapsed[prefix] {
				icon = "chevron-right"
			}
			label := prefix[:len(prefix)-1]
			b.Open("div", "class", "docker-row docker-group", "role", "row")
			b.Open("div", "role", "cell", "class", "docker-group-cell")
			b.Open("button", "class", "docker-group-toggle", "data-ref", ref, "aria-expanded", strconv.FormatBool(!m.collapsed[prefix]), "type", "button")
			b.Element("img", "data-ref", ref+"-icon", "data-pencode-icon", icon, "alt", "")
			b.Text(label + " · " + strconv.Itoa(len(members)) + " 个容器")
			b.End()
			b.End()
			events = append(events, peb.EditorBinding{Ref: ref, Kind: "click"})
			m.writeActions(b, &events, target, "整组 "+prefix[:len(prefix)-1], members)
			if m.collapsed[prefix] {
				continue
			}
		}
		for i := range members {
			c := members[i]
			ports := mappedPorts(c.Ports)
			if ports == "" {
				ports = "—"
			}
			b.Open("div", "class", "docker-row docker-container", "role", "row")
			b.Open("span", "class", "docker-state docker-state-"+c.State, "role", "cell", "title", c.State, "aria-label", c.State)
			b.Element("span", "class", "docker-state-dot", "aria-hidden", "true")
			b.End()
			b.Open("div", "role", "cell")
			b.TextElement("button", c.Names, "class", "docker-name", "data-ref", "logs:"+c.ID, "type", "button", "title", "查看日志 · "+c.Names)
			b.End()
			b.TextElement("span", ports, "class", "docker-ports", "role", "cell", "title", ports)
			b.TextElement("span", c.Status, "class", "docker-status", "role", "cell", "title", c.Status)
			events = append(events, peb.EditorBinding{Ref: "logs:" + c.ID, Kind: "click"})

			m.writeActions(b, &events, c.ID, c.Names, members[i:i+1])
		}
	}
	b.End()
	b.End()
	b.Open("footer", "class", "docker-muted")
	b.Text("点击名称查看日志 · 每 3 秒自动刷新 · ")
	b.Text(strconv.Itoa(len(m.containers)) + " 个容器")
	b.End()
	b.End()
	return m.publisher.Show(peb.UI{ID: "containers", Title: "Docker 容器", Kind: peb.Popup, Anchor: "containers", Context: m.listContext,
		CloseOnOutsideClick: true, Placement: peb.UIPlacement{Mode: peb.PositionAnchor, X: -760, Y: 8, Width: 800, Height: 540},
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: events}})
}

func (m *manager) writeActions(b *markup.Builder, events *[]peb.EditorBinding, target, name string, members []container) {
	b.Open("div", "class", "docker-actions", "role", "cell")
	for _, action := range [...]struct{ id, icon string }{
		{"start", "debug-start"}, {"stop", "debug-stop"}, {"restart", "refresh-cw"},
	} {
		ref := action.id + ":" + target
		disabled := m.busyAction != "" || !slices.ContainsFunc(members, func(c container) bool { return c.allows(action.id) })
		iconButton(b, ref, action.icon, actionLabels[action.id]+" · "+name, disabled)
		*events = append(*events, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	b.End()
	b.End()
}

// Log snapshots reuse the same options until the container list or selection changes.
func (m *manager) updateLogOptions() bool {
	b := markup.Acquire()
	defer b.Release()
	found := false
	for _, c := range m.containers {
		option := b.TextElement("option", c.Names, "value", c.ID)
		if c.ID == m.selected.ID {
			option.SetAttr("selected", "")
			m.selected = c
			found = true
		}
	}
	if !found {
		b.TextElement("option", m.selected.Names+"（已移除）", "selected", "", "disabled", "")
	}
	options := b.String()
	if options == m.logOptions {
		return false
	}
	m.logOptions = options
	return true
}

func (m *manager) showLogs() error {
	m.logLoadingShown = m.logLoading && m.logs == ""
	b := markup.Acquire()
	defer b.Release()
	label, icon := "暂停自动刷新", "debug-pause"
	if m.paused {
		label, icon = "恢复自动刷新", "debug-start"
	}
	b.Open("section", "class", "docker-logs", "data-pencode-layout", "fill")
	b.Open("header", "class", "docker-log-header")
	b.TextElement("strong", "Docker 日志", "class", "docker-log-title")
	b.Open("select", "class", "docker-log-container", "data-ref", "log-container", "aria-label", "选择日志容器")
	b.Embed(m.logOptions)
	b.End()
	b.TextElement("span", "最近 300 行 · 含时间戳", "class", "docker-log-hint docker-muted")
	b.Open("div", "class", "docker-actions", "data-ref", "log-actions", "data-pencode-region", "", "role", "group", "aria-label", "日志操作")
	iconButton(b, "pause", icon, label, false)
	iconButton(b, "refresh-logs", "refresh-cw", "刷新日志", false)
	iconButton(b, "clear-logs", "clear-all", "清屏", m.logs == "" && m.logError == "")
	b.End()
	b.End()
	b.Open("div", "data-ref", "log-notice", "data-pencode-region", "")
	if m.logError != "" {
		b.TextElement("p", m.logError, "class", "docker-error", "role", "alert")
	}
	if m.logs == "" {
		message := "暂无日志。"
		if m.logLoadingShown {
			message = "正在读取日志…"
		}
		b.TextElement("p", message, "class", "docker-muted", "role", "status")
	}
	b.End()
	b.Open("div", "class", "docker-log-body", "data-ref", "log-output", "data-pencode-region", "")
	b.Open("textarea", "class", "docker-log-terminal", "readonly", "", "wrap", "off", "data-ref", "terminal", "aria-label", "容器日志", "data-pencode-terminal-output", m.selected.ID)
	b.Text(m.logs)
	b.End()
	b.End()
	b.End()
	return m.publisher.Show(peb.UI{ID: "logs", Title: "Docker 日志 · " + m.selected.Names, Kind: peb.Panel, Context: m.logContext,
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: []peb.EditorBinding{{Ref: "log-container", Kind: "change"}, {Ref: "pause", Kind: "click"}, {Ref: "refresh-logs", Kind: "click"}, {Ref: "clear-logs", Kind: "click"}}}})
}
