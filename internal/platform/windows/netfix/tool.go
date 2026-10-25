//go:build windows

package netfix

import "winfix/internal/core"

// Tool описывает утилиту для главного меню и командной строки.
func Tool() core.Tool {
	return core.Tool{
		Name:    "network",
		Aliases: []string{"netfix", "net"},
		Title:   "Сеть: диагностика и починка",
		Summary: "Диагностика сети «одной кнопкой» и безопасные способы починки: шлюз, DNS, HTTP, прокси, сброс стека, подбор быстрого DNS.",
		Details: `Порядок при проблемах: 1) диагностика — смотрите, на каком уровне ломается (адаптер → шлюз → интернет по IP → DNS → HTTP);
2) мягкие меры: очистить DNS-кэш → перезапустить адаптер → обновить IP; 3) жёсткие сбросы (Winsock, TCP/IP, полный) — только если мягкие не помогли.
Если активен VPN/туннель, утилита это покажет и предупредит перед каждым жёстким сбросом.`,
		Commands: []core.Command{
			{Name: "diag", Title: "Диагностика (адаптеры, шлюз, DNS, HTTP, прокси, ошибки)",
				Hint: "Проверяет всю цепочку: адаптеры и адреса, пинг шлюза и интернета, разрешение имён, HTTP(S), прокси, hosts, сетевые ошибки за 7 дней, Wi‑Fi. Ничего не меняет — начинайте с неё.",
				Run:  func([]string) { diagnose() }},
			{Name: "flush-dns", Title: "Очистить DNS-кэш",
				Hint: "ipconfig /flushdns. Безопасно. Помогает, когда сайт «не находится» после смены адреса или DNS.",
				Run:  func([]string) { flushDNS() }},
			{Name: "dns", Title: "Подобрать быстрый DNS / сменить DNS",
				Hint: "Сравнивает системный DNS, Cloudflare, Google, Quad9, Яндекс и AdGuard по скорости и сбоям; по желанию ставит выбранный на физический адаптер.",
				Run:  func([]string) { dnsBench() }},
			{Name: "traffic", Title: "Трафик и соединения по процессам",
				Hint: "Сколько принято/отправлено по адаптерам и у каких программ больше всего соединений. Помогает найти, что «ест» интернет.",
				Run:  func([]string) { traffic() }},
			{Name: "dhcp", Title: "Обновить IP-адрес (DHCP renew)", Admin: true,
				Hint: "ipconfig /release + /renew: получить адрес у роутера заново. Соединение пропадёт на несколько секунд.",
				Run:  func([]string) { renewDHCP() }},
			{Name: "adapter", Title: "Перезапустить сетевой адаптер", Admin: true,
				Hint: "Выключает и включает выбранный физический адаптер (Wi‑Fi или Ethernet). Мягкая мера при «подключено, но нет интернета».",
				Run:  func([]string) { restartAdapter() }},
			{Name: "tcp", Title: "Настройки TCP (автонастройка, RSS, ECN)", Admin: true,
				Hint: "Показывает глобальные параметры TCP и по желанию возвращает рекомендуемые: автонастройка окна normal, RSS включён, ECN выключен.",
				Run:  func([]string) { tcpTuning() }},
			{Name: "winsock", Title: "Сброс Winsock", Admin: true,
				Hint: "Лечит «подключено, но сайты не открываются» после VPN и антивирусов. Нужна перезагрузка; может сбить VPN.",
				Run:  func([]string) { resetWinsock() }},
			{Name: "ip-reset", Title: "Сброс стека TCP/IP", Admin: true,
				Hint: "Лечит пропадающий интернет и сбитые настройки TCP/IP. Сбросит статические адреса на автоматические — запишите их заранее. Нужна перезагрузка.",
				Run:  func([]string) { resetIP() }},
			{Name: "proxy", Title: "Сброс системного прокси WinHTTP", Admin: true,
				Hint: "netsh winhttp reset proxy: убирает прокси, «застрявший» после VPN или программ. Режим TUN не затрагивает.",
				Run:  func([]string) { resetProxy() }},
			{Name: "full-reset", Title: "ПОЛНЫЙ сброс сети", Admin: true,
				Hint: "DNS-кэш + Winsock + TCP/IP + DHCP одним действием и предложение перезагрузки. Крайняя мера, когда мягкие способы не помогли.",
				Run:  func([]string) { fullReset() }},
		},
	}
}
