package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Полноэкранное меню (bubbletea): стрелки, подсветка пункта, панель подсказки
// выбранного пункта. Выбор команды закрывает экран — сама команда выполняется
// в обычном окне терминала, где работают ввод, подтверждения и спиннер.

var (
	cCyan   = lipgloss.Color("51")
	cGreen  = lipgloss.Color("42")
	cYellow = lipgloss.Color("220")
	cGray   = lipgloss.Color("244")
	cDim    = lipgloss.Color("238")

	sSel   = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cCyan).Bold(true)
	sGray  = lipgloss.NewStyle().Foreground(cGray)
	sDim   = lipgloss.NewStyle().Foreground(cDim)
	sGreen = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	sYel   = lipgloss.NewStyle().Foreground(cYellow)
	sBold  = lipgloss.NewStyle().Bold(true)
	sNum   = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
)

var gradient = []string{"33", "39", "45", "51", "87", "123", "87", "51", "45", "39"}

func rainbow(s string, frame int) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		c := gradient[(i+frame/2)%len(gradient)]
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(string(r)))
	}
	return b.String()
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type tuiModel struct {
	o      MenuOpts
	cursor int // 0..len(items); len(items) — пункт «выход»
	offset int
	frame  int
	w, h   int
	num    string // набираемый номер пункта
	chosen int    // -1 — выход
	done   bool
}

// tuiSelect показывает меню и возвращает индекс выбранного пункта (-1 — выход)
// и позицию курсора, чтобы вернуться на то же место после выполнения команды.
func tuiSelect(o MenuOpts, cursor int) (int, int) {
	m := tuiModel{o: o, cursor: cursor, chosen: -1, w: 80, h: 30}
	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil { // терминал не подошёл — откатываемся к простому меню
		classicForced = true
		return classicSelect(o), cursor
	}
	fm := res.(tuiModel)
	return fm.chosen, fm.cursor
}

func (m tuiModel) Init() tea.Cmd { return tick() }

func (m tuiModel) count() int { return len(m.o.Items) + 1 }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.frame++
		return m, tick()
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		k := msg.String()
		n := m.count()
		if len(k) == 1 && k[0] >= '0' && k[0] <= '9' {
			nb := m.num + k
			if v, _ := strconv.Atoi(nb); v < 1 || v > len(m.o.Items) {
				nb = k
			}
			m.num = nb
			v, _ := strconv.Atoi(nb)
			switch {
			case v >= 1 && v <= len(m.o.Items):
				m.cursor = v - 1
			case v == 0:
				m.cursor = n - 1
			}
			return m, nil
		}
		m.num = ""
		switch k {
		case "ctrl+c", "q", "esc":
			m.chosen = -1
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor - 1 + n) % n
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % n
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = n - 1
		case "pgup":
			m.cursor = max(0, m.cursor-5)
		case "pgdown":
			m.cursor = min(n-1, m.cursor+5)
		case "enter", " ", "right", "l":
			if m.cursor >= len(m.o.Items) {
				m.chosen = -1
			} else {
				m.chosen = m.cursor
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	inner := m.w - 10 // рамка и отступы
	if inner > 76 {
		inner = 76
	}
	if inner < 40 {
		inner = 40
	}
	wrap := func(s string, c lipgloss.Style) []string {
		var out []string
		for _, l := range Wrap(s, inner) {
			out = append(out, c.Render(l))
		}
		return out
	}

	// максимальная высота подсказки (резервируем, чтобы меню не «прыгало»)
	maxTip := 1
	for _, it := range m.o.Items {
		l := len(Wrap(it.Hint, inner))
		if it.Cmd != "" {
			l++
		}
		if l > maxTip {
			maxTip = l
		}
	}
	n := m.count()

	// layout собирает шапку и подсказку; compact убирает всё необязательное,
	// чтобы весь список поместился в невысокое окно.
	layout := func(compact bool) (head, tip []string, vis int) {
		if m.o.Brand != "" {
			head = append(head, rainbow("▌ "+strings.ToUpper(m.o.Brand)+" ▐", m.frame))
			if !compact {
				for _, s := range m.o.Status {
					head = append(head, sGray.Render(s))
				}
				head = append(head, "")
			}
		}
		head = append(head, sBold.Render(m.o.Title))
		if m.o.Header != "" && !compact {
			head = append(head, wrap(m.o.Header, sGray)...)
		}
		if !compact {
			head = append(head, "")
		}

		if m.cursor < len(m.o.Items) {
			it := m.o.Items[m.cursor]
			tip = wrap(it.Hint, lipgloss.NewStyle())
			if it.Admin && !compact {
				tip = append(tip, sYel.Render("● требует прав администратора"))
			}
			if it.Cmd != "" {
				tip = append(tip, sDim.Render("⌨ ")+sGray.Render(it.Cmd))
			}
		} else {
			tip = []string{sGray.Render(m.o.Exit)}
		}
		reserve := maxTip + 1
		if compact {
			reserve = min(maxTip, 4)
		}
		for len(tip) < reserve {
			tip = append(tip, "")
		}
		overhead := len(head) + len(tip) + 3 /*разделители, подвал*/ + 2 /*рамка*/
		if !compact {
			overhead += 2 /*отступы*/
		}
		return head, tip, m.h - overhead
	}
	head, tipLines, vis := layout(false)

	if vis < n {
		head, tipLines, vis = layout(true)

	}

	foot := sDim.Render("↑↓ выбор · enter запуск · цифры — номер · q — " + strings.ToLower(m.o.Exit))

	if vis < 5 {
		vis = 5
	}
	if vis > n {
		vis = n
	}
	off := m.offset
	if m.cursor < off {
		off = m.cursor
	}
	if m.cursor >= off+vis {
		off = m.cursor - vis + 1
	}
	m.offset = off

	var rows []string
	for i := off; i < off+vis && i < n; i++ {
		label, num, extra := m.o.Exit, "0", ""
		if i < len(m.o.Items) {
			it := m.o.Items[i]
			label, num = it.Title, strconv.Itoa(i+1)
			if it.Admin {
				extra = " " + sYel.Render("●")
			}
		}
		label = Trunc(label, inner-8)
		if i == m.cursor {
			rows = append(rows, " "+sSel.Render(fmt.Sprintf(" ▸ %2s  %s ", num, label))+extra)
		} else {
			rows = append(rows, "   "+sNum.Render(fmt.Sprintf(" %2s", num))+"  "+label+extra)
		}
	}
	if off > 0 {
		rows[0] = sDim.Render("   ↑ ещё выше")
	}
	if off+vis < n {
		rows[len(rows)-1] = sDim.Render("   ↓ ещё ниже")
	}

	rule := sDim.Render(strings.Repeat("─", inner))
	body := strings.Join(head, "\n") + "\n" + strings.Join(rows, "\n") + "\n" + rule + "\n" +
		strings.Join(tipLines, "\n") + "\n" + rule + "\n" + foot

	border := lipgloss.RoundedBorder()
	if !fancy {
		border = lipgloss.NormalBorder()
	}
	return lipgloss.NewStyle().Border(border).BorderForeground(lipgloss.Color("39")).Padding(0, 2).Render(body)
}
