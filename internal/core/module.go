// Package core — ядро WinFix: описание утилит и команд, платформенный модуль
// и приложение (меню, разбор командной строки, справка). Не зависит от ОС:
// всё, что умеет только одна система, живёт в модуле internal/platform/<os>.
package core

// ElevatedFlag добавляется к аргументам при перезапуске с повышением прав.
// Окно с таким запуском закрывается само, поэтому в конце ждём Enter.
const ElevatedFlag = "--elevated"

// Command — действие утилиты: пункт её меню и одновременно подкоманда CLI.
type Command struct {
	Name  string // имя в командной строке: winfix <утилита> <имя>
	Title string // название пункта меню
	Hint  string // что делает пункт и зачем он нужен (показывается в меню)
	Usage string // параметры командной строки (для справки), необязательно
	Admin bool   // требует прав администратора
	// Run выполняет действие. args — параметры командной строки после имени
	// команды; в меню передаётся nil.
	Run func(args []string)
}

// Tool — утилита: группа команд на одну тему (сеть, память, драйверы…).
type Tool struct {
	Name    string   // основное имя: winfix <имя>
	Aliases []string // прежние и сокращённые имена
	Title   string   // название в главном меню
	Summary string   // подсказка в главном меню
	Details string   // подробное описание для справки
	// Commands — команды утилиты. Если команда одна, утилита запускает её сразу,
	// без промежуточного меню.
	Commands []Command
}

// Module — поддержка одной операционной системы.
type Module interface {
	ID() string    // "windows", позже "linux", "darwin"
	Title() string // "Windows"
	// Init настраивает консоль (кодировка, цвета, заголовок окна).
	Init(title string)
	Tools() []Tool

	IsAdmin() bool
	// Elevate перезапускает программу с правами администратора (root).
	// true — запрос ушёл и текущий процесс надо завершить.
	Elevate() bool
	// OpenPath открывает папку в файловом менеджере.
	OpenPath(dir string) error
}

// Installer — необязательная возможность модуля: установка программы в систему.
type Installer interface {
	Install() error
	Uninstall() error
}
