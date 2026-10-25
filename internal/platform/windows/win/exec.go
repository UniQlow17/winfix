//go:build windows

package win

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"winfix/internal/ui"
)

const psPrefix = "$ErrorActionPreference='Continue';$ProgressPreference='SilentlyContinue';" +
	"[Console]::OutputEncoding=[Text.Encoding]::UTF8;"

func hidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// DetachedCmd запускает команду cmd.exe в фоне без окна, не дожидаясь завершения.
// Текст передаётся как есть (без экранирования кавычек Go), как его ждёт cmd.
func DetachedCmd(line string) error {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CmdLine: `cmd.exe /d /c "` + line + `"`}
	return cmd.Start()
}

// Run запускает программу и возвращает объединённый stdout+stderr.
func Run(timeout time.Duration, name string, args ...string) (string, error) {
	defer ui.Busy("").Stop()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	hidden(cmd)
	b, err := cmd.CombinedOutput()
	return DecodeOutput(b), err
}

func psArgs(script string) []string {
	u := utf16.Encode([]rune(psPrefix + script))
	buf := make([]byte, 0, len(u)*2)
	for _, c := range u {
		buf = append(buf, byte(c), byte(c>>8))
	}
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(buf)}
}

// PS выполняет PowerShell-скрипт и возвращает stdout (UTF-8).
// При ошибке в err добавляется stderr.
func PS(timeout time.Duration, script string) (string, error) {
	defer ui.Busy("").Stop()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", psArgs(script)...)
	hidden(cmd)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	s := strings.TrimSpace(DecodeOutput(so.Bytes()))
	if err != nil {
		return s, fmt.Errorf("%w: %s", err, strings.TrimSpace(DecodeOutput(se.Bytes())))
	}
	return s, nil
}

// PSJSON выполняет скрипт, который печатает JSON, и разбирает его в v.
func PSJSON(timeout time.Duration, script string, v any) error {
	s, err := PS(timeout, script)
	if s == "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("пустой ответ PowerShell")
	}
	if i := strings.IndexAny(s, "[{"); i > 0 {
		s = s[i:]
	}
	if e := json.Unmarshal([]byte(s), v); e != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("не удалось разобрать ответ: %w", e)
	}
	return nil
}

// Stream запускает программу и сразу печатает вывод (для долгих операций).
// Возвращает код выхода.
func Stream(name string, args ...string) (int, error) {
	code, _, err := stream(exec.Command(name, args...))
	return code, err
}

// StreamText — как Stream, но дополнительно возвращает весь напечатанный текст.
func StreamText(name string, args ...string) (int, string, error) {
	return stream(exec.Command(name, args...))
}

// StreamPS — то же для PowerShell-скрипта.
func StreamPS(script string) (int, error) {
	code, _, err := stream(exec.Command("powershell.exe", psArgs(script)...))
	return code, err
}

// StreamStdin запускает программу, подавая ей input на stdin.
func StreamStdin(input string, name string, args ...string) (int, error) {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(input)
	code, _, err := stream(c)
	return code, err
}

func stream(cmd *exec.Cmd) (int, string, error) {
	hidden(cmd)
	r, w, err := os.Pipe()
	if err != nil {
		return -1, "", err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return -1, "", err
	}
	w.Close()
	var d Decoder
	var all strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if s := d.Write(buf[:n]); s != "" {
				ui.Raw(s)
				all.WriteString(s)
			}
		}
		if err != nil {
			break
		}
	}
	r.Close()
	werr := cmd.Wait()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	ui.Raw("\n")
	return code, all.String(), werr
}
