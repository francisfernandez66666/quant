//go:build darwin

// Package notify macOS 桌面通知 — 通过 osascript 调用 Notification Center。
package notify

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

var desktopEnabled = true

func EnableDesktop(b bool) { desktopEnabled = b }

func PushDesktop(title, body string) {
	if !desktopEnabled {
		return
	}
	title = sanitize(title)
	body = sanitize(body)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		log.Printf("desktop notify error: %v", err)
	}
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
