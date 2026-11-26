package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxOutput = 256 << 10

type container struct {
	ID, Names, Image, State, Status, Ports string
}

func (c container) allows(action string) bool {
	switch action {
	case "start":
		return c.State == "created" || c.State == "exited"
	case "stop":
		return c.State == "running" || c.State == "restarting" || c.State == "paused"
	case "restart":
		return c.State == "running" || c.State == "restarting" || c.State == "paused" || c.State == "exited"
	}
	return false
}

// Names are sorted by decodeContainers. Only group complete name components,
// so similarly spelled services such as api and apiserver stay separate.
func containerGroups(sorted []container) iter.Seq2[string, []container] {
	return func(yield func(string, []container) bool) {
		for containers := sorted; len(containers) > 0; {
			first := containers[0].Names
			end := 1
			prefix := ""
			if sep := strings.IndexAny(first, "-_."); sep > 0 {
				for end < len(containers) && strings.HasPrefix(containers[end].Names, first[:sep+1]) {
					end++
				}
				if end > 1 {
					last := containers[end-1].Names
					common := 0
					for common < len(first) && common < len(last) && first[common] == last[common] {
						common++
					}
					prefix = first[:strings.LastIndexAny(first[:common], "-_.")+1]
				}
			}
			if !yield(prefix, containers[:end]) {
				return
			}
			containers = containers[end:]
		}
	}
}

func validID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func decodeContainers(output string) ([]container, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	var containers []container
	for {
		var c container
		if err := decoder.Decode(&c); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("解析 Docker 容器列表：%w", err)
		}
		if !validID(c.ID) {
			return nil, fmt.Errorf("Docker 返回了无效的容器 ID")
		}
		containers = append(containers, c)
	}
	slices.SortFunc(containers, func(a, b container) int { return strings.Compare(a.Names, b.Names) })
	return containers, nil
}

// Docker can emit arbitrarily long log lines. Keep only the bounded tail, while
// continuing to drain both pipes so a verbose child cannot block on its output.
type tailBuffer struct {
	data      []byte
	mu        sync.Mutex
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.data)+n > maxOutput {
		b.truncated = true
		if n >= maxOutput {
			b.data = append(b.data[:0], p[n-maxOutput:]...)
			return n, nil
		}
		keep := maxOutput - n
		copy(b.data, b.data[len(b.data)-keep:])
		b.data = b.data[:keep]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *tailBuffer) text() string { return strings.ToValidUTF8(string(b.data), "�") }

func runDocker(ctx context.Context, args ...string) (string, error) {
	timeout := 15 * time.Second
	if len(args) > 0 && (args[0] == "start" || args[0] == "stop" || args[0] == "restart") {
		timeout = 40 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = time.Second
	var output, diagnostic tailBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	logs := len(args) > 0 && args[0] == "logs"
	if logs {
		// Container stderr is application output too, not a CLI failure.
		cmd.Stderr = &output
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("Docker 命令超时或已取消：%w", ctx.Err())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("未找到 docker 命令，请在工作区服务器安装 Docker CLI")
		}
		detail := diagnostic.text()
		if logs {
			detail = output.text()
		}
		if len(detail) > 4096 {
			detail = strings.ToValidUTF8(detail[len(detail)-4096:], "�")
		}
		return "", fmt.Errorf("Docker 命令失败：%w\n%s", err, strings.TrimSpace(detail))
	}
	if output.truncated {
		if !logs {
			return "", fmt.Errorf("Docker 容器列表超过 256 KiB，请减少当前 Docker context 中的容器数量")
		}
		return "[日志过长，仅显示最近 256 KiB]\n" + output.text(), nil
	}
	return output.text(), nil
}
