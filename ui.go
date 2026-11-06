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

func errorView(message string) *peb.EditorView {
	return &peb.EditorView{HTML: `<p class="docker-error" role="alert">` + html.EscapeString(message) + `</p>`, CSS: themeCSS}
}

func (m *manager) showList() error {
	var b strings.Builder
	b.Grow(512 + len(m.containers)*1000)
	b.WriteString(`<section class="docker-manager"><header><div><strong>容器</strong><span class="docker-muted"> · 当前 Docker context</span></div><button data-ref="refresh" type="button"`)
	if m.listLoading {
		b.WriteString(` disabled`)
	}
	b.WriteString(`>刷新</button></header><div class="docker-notice" data-ref="notice" data-pencode-region>`)
	for _, message := range []string{m.operationError, m.listError} {
		if message != "" {
			b.WriteString(`<p class="docker-error" role="alert">` + html.EscapeString(message) + `</p>`)
		}
	}
	if m.busyID != "" {
		b.WriteString(`<p class="docker-muted" role="status">正在` + actionLabels[m.busyAction] + `容器…</p>`)
	}
	b.WriteString(`</div><div class="docker-containers" data-ref="container-list" data-pencode-scroll="preserve" data-pencode-region>`)
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
		fmt.Fprintf(&b, `<article class="docker-container"><button class="docker-summary" data-ref="logs:%s" type="button" title="查看容器日志"><div class="docker-name"><span class="docker-state docker-state-%s">%s</span><strong>%s</strong></div><div class="docker-muted">%s · %s</div><div class="docker-muted">%s</div>`, c.ID, state, state, html.EscapeString(c.Names), html.EscapeString(c.Image), c.ID[:12], html.EscapeString(c.Status))
		if c.Ports != "" {
			b.WriteString(`<div class="docker-muted">` + html.EscapeString(c.Ports) + `</div>`)
		}
		b.WriteString(`</button><div class="docker-actions">`)
		events = append(events, peb.EditorBinding{Ref: "logs:" + c.ID, Kind: "click"})
		for _, action := range [...]string{"start", "stop", "restart"} {
			ref := action + ":" + c.ID
			fmt.Fprintf(&b, `<button data-ref="%s" type="button" title="%s容器"`, ref, actionLabels[action])
			if m.busyID != "" || !c.allows(action) {
				b.WriteString(` disabled`)
			}
			b.WriteString(`>` + actionLabels[action] + `</button>`)
			events = append(events, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		b.WriteString(`</div></article>`)
	}
	b.WriteString(`</div><footer class="docker-muted">点击容器查看日志 · 每 3 秒自动刷新 · `)
	fmt.Fprintf(&b, `%d 个容器</footer></section>`, len(m.containers))
	return m.publisher.Show(peb.UI{ID: "containers", Title: "Docker 容器", Kind: peb.Popup, Anchor: "containers", Context: m.listContext,
		CloseOnOutsideClick: true, Placement: peb.UIPlacement{Mode: peb.PositionAnchor, X: -700, Y: 8, Width: 740, Height: 540},
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: events}})
}

func (m *manager) showLogs() error {
	var b strings.Builder
	b.Grow(len(m.logs) + 768)
	label := "暂停自动刷新"
	if m.paused {
		label = "恢复自动刷新"
	}
	b.WriteString(`<section class="docker-logs"><header><button data-ref="back" type="button">容器列表</button><strong>` + html.EscapeString(m.selected.Names) + `</strong><span class="docker-muted">最近 300 行 · 含时间戳</span><div class="docker-actions"><button data-ref="pause" type="button">` + label + `</button><button data-ref="refresh-logs" type="button">刷新</button></div></header><div data-ref="log-notice" data-pencode-region>`)
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
	b.WriteString(`</div><div class="docker-log-scroll" data-ref="log-scroll" data-pencode-scroll="end" data-pencode-scroll-key="` + m.selected.ID + `" data-pencode-selectable><pre data-ref="log-output" data-pencode-region>`)
	b.WriteString(html.EscapeString(m.logs))
	b.WriteString(`</pre></div></section>`)
	return m.publisher.Show(peb.UI{ID: "logs", Title: "Docker 日志 · " + m.selected.Names, Kind: peb.Panel, Context: m.logContext,
		View: &peb.EditorView{HTML: b.String(), CSS: themeCSS, Events: []peb.EditorBinding{{Ref: "back", Kind: "click"}, {Ref: "pause", Kind: "click"}, {Ref: "refresh-logs", Kind: "click"}}}})
}
