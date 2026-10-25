// Package ui — консольный интерфейс WinFix, не зависящий от ОС:
// цвета, заголовки, баннер, меню с подсказками, спиннер, прогресс-бары,
// таблицы, ввод и структурированный журнал.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ANSI-цвета. При выводе не в терминал (файл, конвейер) они вырезаются.
const (
	Reset   = "\x1b[0m"
	Bold    = "\x1b[1m"
	Red     = "\x1b[31m"
	Green   = "\x1b[32m"
	Yellow  = "\x1b[33m"
	Blue    = "\x1b[34m"
	Magenta = "\x1b[35m"
	Cyan    = "\x1b[36m"
	Gray    = "\x1b[90m"
)

const lineWidth = 72

var (
	stdin  = bufio.NewReader(os.Stdin)
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

	mu      sync.Mutex // защищает вывод, спиннер и журнал
	color   bool       // выводить ANSI-цвета
	fancy   bool       // Unicode-значки и рамки (современный терминал)
	animate bool       // крутить спиннер

	logFile *os.File
	logPart string // незавершённая строка журнала (потоковый вывод)
)

func init() {
	fi, err := os.Stdout.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	color = tty && os.Getenv("NO_COLOR") == ""
	animate = tty
	// Windows Terminal, VS Code, ConEmu и т.п. нормально рисуют Unicode;
	// в классической консоли оставляем безопасные ASCII-метки.
	fancy = tty && (os.Getenv("WT_SESSION") != "" || os.Getenv("TERM_PROGRAM") != "" ||
		os.Getenv("ConEmuANSI") == "ON" || os.Getenv("TERM") != "")
	if os.Getenv("FORCE_COLOR") != "" { // цвета и Unicode даже в конвейере (для проверки вида)
		color, fancy = true, true
	}
	if os.Getenv("WINFIX_ASCII") != "" {
		fancy = false
	}
}

// SetTerminal вызывается модулем ОС после настройки консоли: vt=false означает,
// что консоль не понимает ANSI-последовательности (цвета и анимация отключаются).
func SetTerminal(vt bool) {
	mu.Lock()
	defer mu.Unlock()
	if !vt {
		color, animate, fancy = false, false, false
	}
}

// --- Низкоуровневый вывод

// screen печатает на экран (без записи в журнал).
func screen(s string) {
	mu.Lock()
	defer mu.Unlock()
	screenLocked(s)
}

func screenLocked(s string) {
	if spin.shown {
		os.Stdout.WriteString("\r\x1b[K")
		spin.shown = false
	}
	if !color {
		s = ansiRe.ReplaceAllString(s, "")
	}
	os.Stdout.WriteString(s)
}

// OpenLog открывает журнал <dir>/<name>-<дата>.log.
// Формат строки: «ЧЧ:ММ:СС УРОВЕНЬ текст», цвета удалены.
func OpenLog(dir, name string) {
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	file := fmt.Sprintf("%s-%s.log", name, time.Now().Format("20060102"))
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	logFile = f
	fmt.Fprintf(f, "\n===== %s · сеанс начат =====\n", time.Now().Format("2006-01-02 15:04:05"))
}

// logLine пишет в журнал строки text с уровнем level.
func logLine(level, text string) {
	if logFile == nil {
		return
	}
	text = ansiRe.ReplaceAllString(text, "")
	ts := time.Now().Format("15:04:05")
	for _, l := range strings.Split(strings.TrimRight(text, "\r\n"), "\n") {
		if i := strings.LastIndex(l, "\r"); i >= 0 { // «прогресс» с возвратом каретки — берём итог
			l = l[i+1:]
		}
		fmt.Fprintf(logFile, "%s %-5s %s\n", ts, level, strings.TrimRight(l, " \r"))
	}
}

// logStream пишет потоковый вывод программ построчно.
func logStream(s string) {
	if logFile == nil {
		return
	}
	logPart += ansiRe.ReplaceAllString(s, "")
	for {
		i := strings.Index(logPart, "\n")
		if i < 0 {
			break
		}
		if line := strings.TrimRight(logPart[:i], "\r "); line != "" {
			logLine("OUT", line)
		}
		logPart = logPart[i+1:]
	}
}

func emit(level, text, screenText string) {
	mu.Lock()
	defer mu.Unlock()
	screenLocked(screenText)
	logLine(level, text)
}

// Raw печатает строку как есть (потоковый вывод программ) и пишет её в журнал.
func Raw(s string) {
	mu.Lock()
	defer mu.Unlock()
	screenLocked(s)
	logStream(s)
}

func Printf(f string, a ...any) { Raw(fmt.Sprintf(f, a...)) }

// --- Сообщения

func badge(c, fancyIcon, tag string) string {
	if fancy {
		return c + Bold + " " + fancyIcon + " " + Reset
	}
	return c + tag + Reset
}

// Header — заголовок раздела: ━━ Заголовок ━━━━━━━━━━━━.
func Header(t string) {
	fill := lineWidth - utf8.RuneCountInString(t) - 4
	if fill < 3 {
		fill = 3
	}
	var line string
	if fancy {
		line = fmt.Sprintf("\n%s%s━━ %s %s%s%s\n", Bold, Cyan, t, Reset+Gray, strings.Repeat("━", fill), Reset)
	} else {
		line = fmt.Sprintf("\n%s%s== %s ==%s\n", Bold, Cyan, t, Reset)
	}
	emit("HEAD", "== "+t+" ==", line)
}

func Info(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	emit("INFO", t, "  "+t+"\n")
}

func Dim(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	emit("INFO", t, "  "+Gray+t+Reset+"\n")
}

func OK(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	emit("OK", t, " "+badge(Green, "✔", "[ OK ]")+" "+t+"\n")
}

func Warn(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	emit("WARN", t, " "+badge(Yellow, "⚠", "[ !! ]")+" "+Yellow+t+Reset+"\n")
}

func Fail(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	emit("FAIL", t, " "+badge(Red, "✖", "[FAIL]")+" "+Red+t+Reset+"\n")
}

// Step — начало этапа; его текст становится подписью спиннера.
func Step(f string, a ...any) {
	t := fmt.Sprintf(f, a...)
	SetStatus(t)
	arrow := "➜"
	if !fancy {
		arrow = ">>"
	}
	emit("STEP", t, " "+Cyan+Bold+" "+arrow+Reset+" "+Bold+t+Reset+"\n")
}

// Rule — тонкая разделительная линия.
func Rule() {
	r := "─"
	if !fancy {
		r = "-"
	}
	emit("INFO", "", "  "+Gray+strings.Repeat(r, lineWidth-2)+Reset+"\n")
}

// Banner — рамка с названием программы и строками состояния.
func Banner(title string, lines ...string) {
	tl, tr, bl, br, h, v := "╔", "╗", "╚", "╝", "═", "║"
	if !fancy {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	inner := lineWidth - 6
	row := func(s string, c string) string {
		pad := inner - utf8.RuneCountInString(s)
		if pad < 0 {
			pad = 0
		}
		return "  " + Cyan + v + Reset + " " + c + s + Reset + strings.Repeat(" ", pad) + " " + Cyan + v + Reset + "\n"
	}
	var sb strings.Builder
	sb.WriteString("\n  " + Cyan + tl + strings.Repeat(h, inner+2) + tr + Reset + "\n")
	mark := "◆ "
	if !fancy {
		mark = "* "
	}
	sb.WriteString(row(mark+title, Bold))
	for _, l := range lines {
		sb.WriteString(row("  "+l, Gray))
	}
	sb.WriteString("  " + Cyan + bl + strings.Repeat(h, inner+2) + br + Reset + "\n")
	emit("HEAD", title+" "+strings.Join(lines, " | "), sb.String())
}

// --- Индикаторы

// Bar рисует полосу заполнения шириной width; цвет зависит от доли (зелёный → жёлтый → красный).
func Bar(frac float64, width int) string {
	return barColored(frac, width, 0.8, 0.9)
}

// BarNeutral — полоса без «светофора» (для размеров).
func BarNeutral(frac float64, width int) string {
	return barColored(frac, width, 2, 2)
}

func barColored(frac float64, width int, warn, bad float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac*float64(width) + 0.5)
	c := Green
	switch {
	case frac >= bad:
		c = Red
	case frac >= warn:
		c = Yellow
	case warn > 1:
		c = Cyan
	}
	full, empty := "█", "░"
	if !fancy {
		full, empty = "#", "."
	}
	return c + strings.Repeat(full, n) + Gray + strings.Repeat(empty, width-n) + Reset
}

var spin struct {
	depth  int
	label  string
	start  time.Time
	shown  bool
	status string
	stop   chan struct{}
}

// SetStatus задаёт подпись спиннера по умолчанию.
func SetStatus(s string) {
	mu.Lock()
	spin.status = s
	mu.Unlock()
}

// Spinner — дескриптор запущенного индикатора занятости.
type Spinner struct{ active bool }

// Busy включает анимированный индикатор «идёт работа» до вызова Stop.
// Вложенные и параллельные вызовы безопасны: крутится один индикатор.
// Пустая подпись берётся из последнего ui.Step.
func Busy(label string) *Spinner {
	mu.Lock()
	defer mu.Unlock()
	if label == "" {
		label = spin.status
	}
	if label == "" {
		label = "Выполняю запрос к системе"
	}
	spin.depth++
	if spin.depth == 1 {
		spin.label = label
		spin.start = time.Now()
		if animate {
			spin.stop = make(chan struct{})
			go spinLoop(spin.stop)
		}
	}
	return &Spinner{active: true}
}

// Set меняет подпись индикатора.
func (s *Spinner) Set(label string) {
	mu.Lock()
	spin.label = label
	mu.Unlock()
}

// Stop выключает индикатор.
func (s *Spinner) Stop() {
	if s == nil || !s.active {
		return
	}
	s.active = false
	mu.Lock()
	defer mu.Unlock()
	spin.depth--
	if spin.depth > 0 {
		return
	}
	if spin.stop != nil {
		close(spin.stop)
		spin.stop = nil
	}
	if spin.shown {
		os.Stdout.WriteString("\r\x1b[K")
		spin.shown = false
	}
}

func spinLoop(stop chan struct{}) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if !fancy {
		frames = []string{"|", "/", "-", "\\"}
	}
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-t.C:
			mu.Lock()
			if spin.depth > 0 && spin.stop == stop {
				el := int(time.Since(spin.start).Seconds())
				txt := Trunc(spin.label, lineWidth-14)
				line := fmt.Sprintf("\r\x1b[K  %s%s%s %s %s%ds%s", Cyan, frames[i%len(frames)], Reset, txt, Gray, el, Reset)
				if !color {
					line = fmt.Sprintf("\r  %s %s %ds", frames[i%len(frames)], txt, el)
				}
				os.Stdout.WriteString(line)
				spin.shown = true
			}
			mu.Unlock()
		}
	}
}

// --- Ввод

// ReadLine читает строку из stdin. ok=false при конце ввода.
func ReadLine() (string, bool) {
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// Ask выводит приглашение и читает ответ.
func Ask(prompt string) (string, bool) {
	screen(Bold + Cyan + "? " + Reset + Bold + prompt + Reset + " ")
	s, ok := ReadLine()
	mu.Lock()
	logLine("INPUT", prompt+" "+s)
	mu.Unlock()
	return s, ok
}

// Confirm задаёт вопрос да/нет.
func Confirm(q string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	s, ok := Ask(q + " " + Gray + hint + Reset + Bold + ":")
	if !ok {
		return false
	}
	switch strings.ToLower(s) {
	case "":
		return def
	case "y", "yes", "д", "да":
		return true
	}
	return false
}

// Pause ждёт Enter.
func Pause(msg string) {
	screen(Gray + "\n" + msg + Reset)
	ReadLine()
}

// --- Меню

// Item — пункт меню. Hint объясняет, что делает пункт и зачем он нужен.
type Item struct {
	Title string
	Hint  string
	Cmd   string // эквивалент в командной строке (показывается под подсказкой)
	Admin bool   // пункту нужны права администратора
	Sub   bool   // пункт открывает вложенное меню (после него не ждём Enter)
	Run   func()
}

// MenuOpts описывает экран меню.
type MenuOpts struct {
	Title  string   // заголовок экрана
	Header string   // пояснение под заголовком
	Brand  string   // название программы (для главного меню), необязательно
	Status []string // строки состояния под названием
	Items  []Item
	Exit   string // название пункта 0
}

const hintWidth = 66

var (
	stdinTTY      bool
	bannerShown   bool
	classicForced = os.Getenv("WINFIX_CLASSIC") != ""
)

func init() {
	fi, err := os.Stdin.Stat()
	stdinTTY = err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// useTUI сообщает, можно ли показывать полноэкранное меню со стрелками.
func useTUI() bool { return animate && stdinTTY && !classicForced }

// Menu — краткая форма RunMenu.
func Menu(title, header string, items []Item, exit string) {
	RunMenu(MenuOpts{Title: title, Header: header, Items: items, Exit: exit})
}

// RunMenu показывает меню в цикле, пока пользователь не выберет выход.
// В терминале это полноэкранное меню со стрелками и подсказкой выбранного
// пункта; при выводе в файл/конвейер — простое меню с номерами.
// После действия ждёт Enter, чтобы вывод не терялся.
func RunMenu(o MenuOpts) {
	cursor := 0
	for {
		var idx int
		if useTUI() {
			idx, cursor = tuiSelect(o, cursor)
		} else {
			idx = classicSelect(o)
		}
		if idx < 0 {
			return
		}
		it := o.Items[idx]
		mu.Lock()
		logLine("MENU", it.Title)
		mu.Unlock()
		if useTUI() && !it.Sub {
			ClearScreen()
			screen(Bold + Cyan + "▸ " + it.Title + Reset + "\n")
		}
		it.Run()
		if !it.Sub {
			Pause("Нажмите Enter, чтобы вернуться в меню…")
			if useTUI() {
				ClearScreen()
			}
		}
	}
}

// classicSelect — меню с вводом номера. Возвращает индекс пункта или -1 для выхода.
func classicSelect(o MenuOpts) int {
	for {
		if o.Brand != "" && !bannerShown {
			bannerShown = true
			Banner(o.Brand, o.Status...)
		}
		Header(o.Title)
		if o.Header != "" {
			var sb strings.Builder
			for _, l := range Wrap(o.Header, hintWidth+4) {
				sb.WriteString("  " + Gray + l + Reset + "\n")
			}
			screen(sb.String())
		}
		var sb strings.Builder
		sb.WriteString("\n")
		for i, it := range o.Items {
			mark := ""
			if it.Admin {
				mark = "  " + Yellow + "[админ]" + Reset
			}
			sb.WriteString(fmt.Sprintf("  %s%s%2d%s  %s%s%s%s\n", Cyan, Bold, i+1, Reset, Bold, it.Title, Reset, mark))
			for _, l := range Wrap(it.Hint, hintWidth) {
				sb.WriteString(Gray + "      " + l + Reset + "\n")
			}
			if it.Cmd != "" {
				sb.WriteString(Gray + "      CLI: " + it.Cmd + Reset + "\n")
			}
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("  %s%s 0%s  %s\n", Cyan, Bold, Reset, o.Exit))
		screen(sb.String())

		screen("\n")
		s, ok := Ask("Выберите пункт:")
		if !ok || s == "0" || strings.EqualFold(s, "q") {
			return -1
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(o.Items) {
			Warn("Нет такого пункта.")
			continue
		}
		return n - 1
	}
}

// --- Таблицы

// Table печатает таблицу с выравниванием по рунам и подчёркнутой шапкой.
func Table(headers []string, rows [][]string) {
	vis := func(s string) int { return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, "")) }
	w := make([]int, len(headers))
	for i, h := range headers {
		w[i] = vis(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(w) && vis(c) > w[i] {
				w[i] = vis(c)
			}
		}
	}
	pad := func(s string, n int) string {
		if d := n - vis(s); d > 0 {
			return s + strings.Repeat(" ", d)
		}
		return s
	}
	rule := "─"
	if !fancy {
		rule = "-"
	}
	var sb strings.Builder
	sb.WriteString("  " + Bold + Gray)
	for i, h := range headers {
		sb.WriteString(pad(h, w[i]) + "  ")
	}
	sb.WriteString(Reset + "\n  " + Gray)
	for i := range headers {
		sb.WriteString(strings.Repeat(rule, w[i]) + "  ")
	}
	sb.WriteString(Reset + "\n")
	for _, r := range rows {
		sb.WriteString("  ")
		for i := range headers {
			c := ""
			if i < len(r) {
				c = r[i]
			}
			sb.WriteString(pad(c, w[i]) + "  ")
		}
		sb.WriteString("\n")
	}
	s := sb.String()
	mu.Lock()
	defer mu.Unlock()
	screenLocked(s)
	logLine("TABLE", s)
}

// ClearScreen очищает экран и буфер прокрутки терминала.
func ClearScreen() {
	if animate {
		screen("\x1b[2J\x1b[3J\x1b[H")
	}
}
