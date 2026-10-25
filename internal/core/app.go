package core

import (
	"fmt"
	"os"
	"strings"
	"time"

	"winfix/internal/ui"
)

// App — приложение WinFix для одной ОС.
type App struct {
	Mod     Module
	Version string

	elevated bool // запущены повторно с повышением прав
}

// Run разбирает командную строку и выполняет запрос. Возвращает код выхода.
func (a *App) Run(args []string) int {
	args = a.stripElevated(args)
	a.Mod.Init("WinFix")
	ui.OpenLog(DataDir("logs"), "winfix")

	code := a.dispatch(args)
	if a.elevated && len(args) > 0 {
		// окно с повышенными правами закроется само — даём прочитать результат
		ui.Pause("Нажмите Enter, чтобы закрыть окно…")
	}
	return code
}

func (a *App) stripElevated(args []string) []string {
	var rest []string
	for _, s := range args {
		if s == ElevatedFlag {
			a.elevated = true
			continue
		}
		rest = append(rest, s)
	}
	return rest
}

func (a *App) dispatch(args []string) int {
	if len(args) == 0 {
		a.interactive()
		return 0
	}
	switch strings.ToLower(args[0]) {
	case "-h", "--help", "-?", "/?", "help":
		a.help(args[1:])
		return 0
	case "-v", "--version", "version":
		fmt.Printf("WinFix %s (%s)\n", a.Version, a.Mod.Title())
		return 0
	case "install", "uninstall":
		return a.installCmd(args[0] == "install")
	}

	t := a.findTool(args[0])
	if t == nil {
		ui.Fail("Неизвестная утилита или команда: %s", args[0])
		a.usage()
		return 2
	}
	rest := args[1:]
	if len(rest) == 0 {
		a.elevateForMenu()
		a.runTool(t)
		return 0
	}
	if rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help" {
		a.toolHelp(t)
		return 0
	}
	// У утилиты с единственной командой имя команды можно не указывать.
	cmd := findCommand(t, rest[0])
	if cmd != nil {
		rest = rest[1:]
	} else if len(t.Commands) == 1 {
		cmd = &t.Commands[0]
	} else {
		ui.Fail("У утилиты «%s» нет команды «%s».", t.Name, rest[0])
		a.toolHelp(t)
		return 2
	}
	a.execute(cmd, rest)
	return 0
}

func (a *App) findTool(name string) *Tool {
	tools := a.Mod.Tools()
	for i := range tools {
		t := &tools[i]
		if strings.EqualFold(t.Name, name) {
			return t
		}
		for _, al := range t.Aliases {
			if strings.EqualFold(al, name) {
				return t
			}
		}
	}
	return nil
}

func findCommand(t *Tool, name string) *Command {
	for i := range t.Commands {
		if strings.EqualFold(t.Commands[i].Name, name) {
			return &t.Commands[i]
		}
	}
	return nil
}

// execute запускает команду, при необходимости перезапустив программу от администратора.
func (a *App) execute(c *Command, args []string) {
	if c.Admin && !a.Mod.IsAdmin() && os.Getenv("WINFIX_NO_ELEVATE") == "" {
		ui.Warn("Команде нужны права администратора. Запрашиваю повышение…")
		if a.Mod.Elevate() {
			os.Exit(0)
		}
		ui.Fail("Права администратора не получены — часть функций будет недоступна.")
	}
	start := time.Now()
	c.Run(args)
	ui.Dim("Завершено за %s", elapsed(time.Since(start)))
}

// elevateForMenu один раз за сеанс запрашивает права, чтобы пункты меню не просили их по отдельности.
func (a *App) elevateForMenu() {
	if a.Mod.IsAdmin() || os.Getenv("WINFIX_NO_ELEVATE") != "" {
		return
	}
	ui.Warn("Для большинства функций нужны права администратора. Запрашиваю повышение…")
	if a.Mod.Elevate() {
		os.Exit(0)
	}
	ui.Warn("Работаем без прав администратора: пункты с пометкой [админ] будут недоступны.")
}

// --- Интерактивный режим

func (a *App) interactive() {
	a.elevateForMenu()
	tools := a.Mod.Tools()
	var items []ui.Item
	for i := range tools {
		t := &tools[i]
		items = append(items, ui.Item{Title: t.Title, Hint: t.Summary, Sub: len(t.Commands) > 1, Run: func() { a.runTool(t) }})
	}
	items = append(items,
		ui.Item{Title: "Справка: что умеет каждая утилита", Hint: "Подробное описание утилит и команд для командной строки.",
			Run: func() { a.help(nil) }},
		ui.Item{Title: "Открыть папку отчётов", Hint: "Здесь лежат отчёты «Проверки здоровья» — удобно сравнивать до и после ремонта.",
			Run: func() { a.open("reports") }},
		ui.Item{Title: "Открыть папку журналов", Hint: "Журналы работы WinFix (что именно запускалось и что вывелось) — приложите при обращении за помощью.",
			Run: func() { a.open("logs") }},
	)
	if _, ok := a.Mod.(Installer); ok {
		items = append(items, ui.Item{Title: "Установить WinFix на этот компьютер",
			Hint: "Копирует программу в профиль, добавляет команду winfix в PATH и ярлыки (рабочий стол, меню «Пуск») с запуском от администратора.",
			Run:  func() { a.installCmd(true) }})
	}
	state := "Права администратора: ● нет — часть пунктов недоступна"
	if a.Mod.IsAdmin() {
		state = "Права администратора: ● есть"
	}
	ui.RunMenu(ui.MenuOpts{
		Title: "Главное меню",
		Header: "Начните с «Проверки здоровья» — она покажет, что именно требует внимания, и подскажет нужную утилиту. " +
			"Сначала всегда диагностика и показ результата, потом действие с подтверждением.",
		Brand:  fmt.Sprintf("WinFix %s", a.Version),
		Status: []string{"Обслуживание и диагностика " + a.Mod.Title(), state},
		Items:  items,
		Exit:   "Выход",
	})
}

func (a *App) runTool(t *Tool) {
	if len(t.Commands) == 1 {
		a.execute(&t.Commands[0], nil)
		return
	}
	var items []ui.Item
	for i := range t.Commands {
		c := &t.Commands[i]
		items = append(items, ui.Item{Title: c.Title, Hint: c.Hint, Cmd: "winfix " + t.Name + " " + c.Name, Admin: c.Admin,
			Run: func() { a.execute(c, nil) }})
	}
	ui.RunMenu(ui.MenuOpts{Title: t.Title, Header: t.Summary, Items: items, Exit: "Назад"})
}

func (a *App) open(sub string) {
	dir := DataDir(sub)
	if err := a.Mod.OpenPath(dir); err != nil {
		ui.Fail("Не удалось открыть %s: %v", dir, err)
	}
}

func (a *App) installCmd(install bool) int {
	in, ok := a.Mod.(Installer)
	if !ok {
		ui.Fail("Установка для %s пока не поддерживается: программа работает как есть, из любой папки.", a.Mod.Title())
		return 1
	}
	var err error
	if install {
		err = in.Install()
	} else {
		err = in.Uninstall()
	}
	if err != nil {
		ui.Fail("%v", err)
		return 1
	}
	return 0
}

// --- Справка

func (a *App) usage() {
	fmt.Print(`
Использование:
  winfix                          интерактивное меню (рекомендуется)
  winfix <утилита>                меню одной утилиты
  winfix <утилита> <команда> ...  выполнить команду без меню (для скриптов и планировщика)
  winfix help [утилита]           справка
  winfix install | uninstall      установить на компьютер / удалить
  winfix version                  версия
`)
}

func (a *App) help(args []string) {
	if len(args) > 0 {
		if t := a.findTool(args[0]); t != nil {
			a.toolHelp(t)
			return
		}
		ui.Warn("Утилиты «%s» нет — показываю общую справку.", args[0])
	}
	ui.Header(fmt.Sprintf("WinFix %s — обслуживание %s", a.Version, a.Mod.Title()))
	ui.Info("Один исполняемый файл с набором утилит. Принцип везде одинаков:")
	ui.Info("сначала диагностика и показ результата, потом действие с подтверждением.")
	a.usage()
	ui.Header("Утилиты")
	for _, t := range a.Mod.Tools() {
		ui.Printf("  %s%-8s%s %s%s%s\n", ui.Cyan, t.Name, ui.Reset, ui.Bold, t.Title, ui.Reset)
		for _, l := range ui.Wrap(t.Summary, 72) {
			ui.Printf("           %s%s%s\n", ui.Gray, l, ui.Reset)
		}
	}
	ui.Info("")
	ui.Dim("Подробности по утилите: winfix help <имя>, например: winfix help network")
}

func (a *App) toolHelp(t *Tool) {
	ui.Header(t.Title + "  (winfix " + t.Name + ")")
	if len(t.Aliases) > 0 {
		ui.Dim("Другие имена: %s", strings.Join(t.Aliases, ", "))
	}
	for _, l := range strings.Split(t.Details, "\n") {
		ui.Info("%s", l)
	}
	ui.Header("Команды")
	for _, c := range t.Commands {
		mark := ""
		if c.Admin {
			mark = "  " + ui.Yellow + "[админ]" + ui.Reset
		}
		ui.Printf("  %s%s%s%s\n", ui.Cyan, "winfix "+t.Name+" "+c.Name, ui.Reset, mark)
		ui.Printf("      %s%s%s\n", ui.Bold, c.Title, ui.Reset)
		for _, l := range ui.Wrap(c.Hint, 72) {
			ui.Printf("      %s%s%s\n", ui.Gray, l, ui.Reset)
		}
		if c.Usage != "" {
			ui.Printf("      %sПараметры: %s%s\n", ui.Gray, c.Usage, ui.Reset)
		}
	}
	ui.Info("")
}

func elapsed(d time.Duration) string {
	if d < time.Second {
		return "менее секунды"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d с", int(d.Seconds()))
	}
	return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
}
