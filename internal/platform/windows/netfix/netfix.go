//go:build windows

// netfix — диагностика и починка сети: адаптеры, шлюз, DNS, HTTP, прокси,
// сбросы стека, подбор быстрого DNS, TCP-настройки.
package netfix

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"winfix/internal/platform/windows/win"
	"winfix/internal/ui"
)

type adapter struct {
	Name   string `json:"Name"`
	Desc   string `json:"Desc"`
	Status string `json:"Status"`
	Speed  string `json:"Speed"`
	Index  int    `json:"Index"`
	Virt   bool   `json:"Virt"`
	GW     string `json:"GW"`
	DNS    string `json:"DNS"`
	IP     string `json:"IP"`
}

var tunnelRe = regexp.MustCompile(`(?i)wintun|wireguard|tap-|\btun\b|happ|vpn|tunnel|openvpn|tailscale|zerotier|nordlynx`)

func adapters() ([]adapter, error) {
	var a []adapter
	err := win.PSJSON(60*time.Second, `
$ip=@{}; Get-NetIPConfiguration -ErrorAction SilentlyContinue | ForEach-Object { $ip[[int]$_.InterfaceIndex]=$_ }
$r=@(Get-NetAdapter -IncludeHidden:$false -ErrorAction SilentlyContinue | ForEach-Object {
 $c=$ip[[int]$_.ifIndex]
 [pscustomobject]@{Name="$($_.Name)";Desc="$($_.InterfaceDescription)";Status="$($_.Status)";Speed="$($_.LinkSpeed)";Index=[int]$_.ifIndex;Virt=[bool]$_.Virtual;
  GW=$(if($c -and $c.IPv4DefaultGateway){"$($c.IPv4DefaultGateway.NextHop)"}else{''});
  DNS=$(if($c -and $c.DNSServer){(($c.DNSServer | Where-Object {$_.AddressFamily -eq 2}).ServerAddresses) -join ', '}else{''});
  IP=$(if($c -and $c.IPv4Address){"$($c.IPv4Address.IPAddress)"}else{''})} })
ConvertTo-Json -InputObject $r -Compress`, &a)
	return a, err
}

type pingRes struct{ Sent, Recv, Min, Avg, Max int }

func ping(host string, n int) pingRes {
	var r pingRes
	win.PSJSON(60*time.Second, fmt.Sprintf(`
$r=@(Test-Connection -ComputerName '%s' -Count %d -ErrorAction SilentlyContinue)
$m=$r | Measure-Object -Property ResponseTime -Minimum -Maximum -Average
ConvertTo-Json -Compress -InputObject ([pscustomobject]@{Sent=%d;Recv=$r.Count;Min=[int]$m.Minimum;Avg=[int]$m.Average;Max=[int]$m.Maximum})`, host, n, n), &r)
	return r
}

func reportPing(label, host string) {
	r := ping(host, 5)
	if r.Recv == 0 {
		ui.Fail("%s (%s): нет ответа", label, host)
		return
	}
	loss := (r.Sent - r.Recv) * 100 / r.Sent
	line := fmt.Sprintf("%s (%s): потери %d%%, задержка мин/сред/макс %d/%d/%d мс", label, host, loss, r.Min, r.Avg, r.Max)
	if loss > 0 || r.Avg > 150 || r.Max > 500 {
		ui.Warn("%s", line)
	} else {
		ui.OK("%s", line)
	}
}

func resolver(server string) *net.Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 2 * time.Second}
		return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
	}}
}

func lookup(r *net.Resolver, domain string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t := time.Now()
	_, err := r.LookupHost(ctx, domain)
	return time.Since(t), err
}

func diagnose() {
	ui.Header("Адаптеры и адреса")
	ads, err := adapters()
	if err != nil {
		ui.Fail("Не удалось получить адаптеры: %v", err)
	}
	rows := [][]string{}
	gw := ""
	for _, a := range ads {
		if a.Status != "Up" && a.Virt {
			continue
		}
		kind := ""
		if tunnelRe.MatchString(a.Desc + " " + a.Name) {
			kind = "VPN/туннель"
		} else if a.Virt {
			kind = "виртуальный"
		}
		rows = append(rows, []string{a.Name, ui.Trunc(a.Desc, 38), a.Status, a.Speed, a.IP, a.GW, kind})
		if a.Status == "Up" && a.GW != "" && gw == "" && kind == "" {
			gw = a.GW
		}
	}
	ui.Table([]string{"Имя", "Адаптер", "Статус", "Скорость", "IP", "Шлюз", ""}, rows)
	for _, a := range ads {
		if a.Status == "Up" && a.DNS != "" && !a.Virt {
			ui.Info("DNS на «%s»: %s", a.Name, a.DNS)
		}
		if tunnelRe.MatchString(a.Desc+" "+a.Name) && a.Status == "Up" {
			ui.Warn("Активен VPN/туннель («%s») — трафик и DNS могут идти через него. Сбросы стека могут его сбить.", a.Name)
		}
	}

	ui.Header("Доступность и задержки")
	if gw != "" {
		reportPing("Шлюз", gw)
	} else {
		ui.Warn("Шлюз по умолчанию на физическом адаптере не найден.")
	}
	reportPing("Cloudflare", "1.1.1.1")
	reportPing("Google DNS", "8.8.8.8")

	ui.Header("Разрешение имён (системный DNS)")
	for _, d := range []string{"google.com", "yandex.ru", "microsoft.com", "github.com"} {
		t, err := lookup(net.DefaultResolver, d)
		if err != nil {
			ui.Fail("%s: %v", d, err)
		} else if t > 500*time.Millisecond {
			ui.Warn("%s: %d мс (медленно)", d, t.Milliseconds())
		} else {
			ui.OK("%s: %d мс", d, t.Milliseconds())
		}
	}

	ui.Header("HTTP(S)")
	client := &http.Client{Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, u := range []string{"http://www.msftconnecttest.com/connecttest.txt", "https://www.google.com/generate_204", "https://yandex.ru", "https://github.com"} {
		t := time.Now()
		sp := ui.Busy("Проверяю " + u)
		resp, err := client.Get(u)
		sp.Stop()
		if err != nil {
			ui.Fail("%s: %v", u, ui.Trunc(err.Error(), 80))
			continue
		}
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		el := time.Since(t)
		line := fmt.Sprintf("%s: HTTP %d, %d мс", u, resp.StatusCode, el.Milliseconds())
		switch {
		case strings.Contains(u, "msftconnecttest") && !strings.Contains(string(body[:n]), "Microsoft Connect Test"):
			ui.Warn("%s — ответ не от Microsoft: Windows может показывать «нет интернета» (перехват провайдером/VPN).", line)
		case el > 2*time.Second:
			ui.Warn("%s — медленно (обычно из-за VPN/маршрутизации)", line)
		default:
			ui.OK("%s", line)
		}
	}

	ui.Header("Прокси и hosts")
	out, _ := win.Run(15*time.Second, "netsh.exe", "winhttp", "show", "proxy")
	ui.Info("WinHTTP: %s", strings.TrimSpace(strings.ReplaceAll(out, "\r\n", " ")))
	if s, _ := win.PS(15*time.Second, `$k=Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'; "ProxyEnable=$($k.ProxyEnable); ProxyServer=$($k.ProxyServer); AutoConfigURL=$($k.AutoConfigURL)"`); s != "" {
		if strings.Contains(s, "ProxyEnable=1") || strings.Contains(s, "AutoConfigURL=http") {
			ui.Warn("Прокси пользователя включён: %s", s)
		} else {
			ui.OK("Системный прокси пользователя выключен.")
		}
	}
	if b, err := os.ReadFile(os.Getenv("SystemRoot") + `\System32\drivers\etc\hosts`); err == nil {
		var ent []string
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				ent = append(ent, l)
			}
		}
		if len(ent) == 0 {
			ui.OK("Файл hosts чистый.")
		} else {
			ui.Warn("В hosts %d активных записей (проверьте, что они нужны):", len(ent))
			for i, e := range ent {
				if i >= 10 {
					break
				}
				ui.Info("   %s", ui.Trunc(e, 90))
			}
		}
	}

	ui.Header("Сетевые ошибки в журнале за 7 дней")
	var ev []struct {
		Provider string
		Count    int
		Recent   int
	}
	if err := win.PSJSON(2*time.Minute, `
$h=(Get-Date).AddMinutes(-10)
$r=@(Get-WinEvent -FilterHashtable @{LogName='System';Level=2,3;StartTime=(Get-Date).AddDays(-7)} -ErrorAction SilentlyContinue |
 Where-Object { $_.ProviderName -match 'Tcpip|NDIS|rt25|rtl|e1d|e2f|Netwtw|Netwlan|Dhcp|DNS-Client|WLAN|Wlan|bcm|Netw|mlx|iaLP|NlaSvc|netvsc' } |
 Group-Object ProviderName | ForEach-Object {[pscustomobject]@{Provider=$_.Name;Count=$_.Count;Recent=@($_.Group | Where-Object {$_.TimeCreated -gt $h}).Count}})
ConvertTo-Json -InputObject $r -Compress`, &ev); err == nil {
		if len(ev) == 0 {
			ui.OK("Ошибок сетевых драйверов и служб нет.")
		}
		ongoing := false
		for _, e := range ev {
			if e.Recent > 0 {
				ui.Fail("%s: %d событий за 7 дней, из них %d за последние 10 мин — ошибки ПРОДОЛЖАЮТСЯ", e.Provider, e.Count, e.Recent)
				ongoing = true
			} else {
				ui.Warn("%s: %d событий за 7 дней (за последние 10 мин — нет)", e.Provider, e.Count)
			}
		}
		if ongoing {
			ui.Info("Идущий поток ошибок одного драйвера: отключите проблемный адаптер (если он не нужен) или обновите драйвер (winfix drivers).")
			ui.Info("Если это сетевая карта с «Hardware IO error» (например, Realtek 2.5GbE, rt25cx21) — помогает отключить её, пока не подключён кабель.")
		}
	}

	ui.Header("Wi-Fi")
	w, _ := win.Run(15*time.Second, "netsh.exe", "wlan", "show", "interfaces")
	wre := regexp.MustCompile(`(?i)^\s*(SSID|Состояние|State|Сигнал|Signal|Канал|Channel|Тип радио|Radio type|Скорость|Receive rate|Transmit rate)`)
	n := 0
	for _, l := range strings.Split(w, "\n") {
		if wre.MatchString(l) && !strings.Contains(strings.ToUpper(l), "BSSID") {
			ui.Info("%s", strings.TrimSpace(l))
			n++
		}
	}
	if n == 0 {
		ui.Dim("Wi-Fi-адаптер не активен.")
	}
}

func flushDNS() {
	ui.Header("Очистка DNS-кэша")
	out, err := win.Run(30*time.Second, "ipconfig.exe", "/flushdns")
	ui.Info("%s", strings.TrimSpace(out))
	if err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("Готово.")
}

func admin() bool {
	if win.IsAdmin() {
		return true
	}
	ui.Info("Нужны права администратора: программа перезапустится с запросом UAC — выберите пункт ещё раз.")
	win.RequireAdmin("сетевые настройки")
	return win.IsAdmin()
}

const vpnWarn = "ВНИМАНИЕ: если вы пользуетесь VPN/прокси-клиентом (WireGuard, xray, OpenVPN и т.п.), после сброса может потребоваться перезагрузка и перезапуск VPN."

func renewDHCP() {
	ui.Header("Обновление IP-адреса (DHCP)")
	if !admin() {
		return
	}
	ui.Info("Соединение пропадёт на несколько секунд.")
	if !ui.Confirm("Выполнить ipconfig /release и /renew?", false) {
		return
	}
	win.Stream("ipconfig.exe", "/release")
	win.Stream("ipconfig.exe", "/renew")
	ui.OK("Готово.")
}

func resetWinsock() {
	ui.Header("Сброс Winsock")
	if !admin() {
		return
	}
	ui.Info("Лечит: «подключено, но сайты не открываются», сломанные LSP-фильтры после VPN/антивирусов.")
	ui.Warn(vpnWarn)
	if !ui.Confirm("Выполнить netsh winsock reset?", false) {
		return
	}
	win.Stream("netsh.exe", "winsock", "reset")
	ui.OK("Готово. Нужна перезагрузка.")
}

func resetIP() {
	ui.Header("Сброс стека TCP/IP")
	if !admin() {
		return
	}
	ui.Info("Лечит: пропадающий интернет, неверные настройки TCP/IP. Адреса адаптеров вернутся к автоматическим (DHCP).")
	ui.Warn(vpnWarn)
	ui.Warn("Если у вас задан статический IP — запишите его заранее!")
	if !ui.Confirm("Выполнить netsh int ip reset и int ipv6 reset?", false) {
		return
	}
	win.Stream("netsh.exe", "int", "ip", "reset")
	win.Stream("netsh.exe", "int", "ipv6", "reset")
	ui.OK("Готово. Нужна перезагрузка.")
}

func resetProxy() {
	ui.Header("Сброс системного прокси WinHTTP")
	if !admin() {
		return
	}
	ui.Warn(vpnWarn)
	ui.Info("Это не затрагивает TUN-режим VPN, но отключает прокси, прописанный в WinHTTP.")
	if !ui.Confirm("Выполнить netsh winhttp reset proxy?", false) {
		return
	}
	win.Stream("netsh.exe", "winhttp", "reset", "proxy")
}

func tcpTuning() {
	ui.Header("Настройки TCP")
	out, _ := win.Run(15*time.Second, "netsh.exe", "int", "tcp", "show", "global")
	ui.Info("%s", strings.TrimSpace(out))
	ui.Info("Рекомендуемые значения: автонастройка окна = normal, RSS = enabled, ECN = disabled.")
	if !ui.Confirm("Применить рекомендуемые значения?", false) {
		return
	}
	if !admin() {
		return
	}
	for _, args := range [][]string{
		{"int", "tcp", "set", "global", "autotuninglevel=normal"},
		{"int", "tcp", "set", "global", "rss=enabled"},
		{"int", "tcp", "set", "global", "ecncapability=disabled"},
	} {
		o, err := win.Run(15*time.Second, "netsh.exe", args...)
		if err != nil {
			ui.Warn("%s: %s", strings.Join(args, " "), strings.TrimSpace(o))
		} else {
			ui.OK("%s", strings.Join(args, " "))
		}
	}
}

func dnsBench() {
	ui.Header("Подбор быстрого DNS")
	type srv struct{ name, ip string }
	servers := []srv{{"Системный (текущий)", ""}, {"Cloudflare", "1.1.1.1"}, {"Google", "8.8.8.8"}, {"Quad9", "9.9.9.9"}, {"Яндекс", "77.88.8.8"}, {"AdGuard", "94.140.14.14"}}
	domains := []string{"google.com", "yandex.ru", "microsoft.com", "github.com", "wikipedia.org", "vk.com"}
	type res struct {
		name   string
		ip     string
		median time.Duration
		fails  int
	}
	results := make([]res, len(servers))
	var wg sync.WaitGroup
	ui.Step("Измеряю (несколько секунд)…")
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s srv) {
			defer wg.Done()
			r := resolver(s.ip)
			var ts []time.Duration
			fails := 0
			for round := 0; round < 2; round++ {
				for _, d := range domains {
					t, err := lookup(r, d)
					if err != nil {
						fails++
						continue
					}
					ts = append(ts, t)
				}
			}
			sort.Slice(ts, func(a, b int) bool { return ts[a] < ts[b] })
			var med time.Duration
			if len(ts) > 0 {
				med = ts[len(ts)/2]
			}
			results[i] = res{s.name, s.ip, med, fails}
		}(i, s)
	}
	sp := ui.Busy("Измеряю скорость DNS-серверов")
	wg.Wait()
	sp.Stop()
	rows := [][]string{}
	best := -1
	for i, r := range results {
		med := "—"
		if r.median > 0 {
			med = fmt.Sprintf("%d мс", r.median.Milliseconds())
		}
		ip := r.ip
		if ip == "" {
			ip = "(из настроек адаптера)"
		}
		rows = append(rows, []string{strconv.Itoa(i + 1), r.name, ip, med, strconv.Itoa(r.fails)})
		if r.ip != "" && r.median > 0 && r.fails <= 2 && (best < 0 || r.median < results[best].median) {
			best = i
		}
	}
	ui.Table([]string{"№", "Сервер", "Адрес", "Медиана", "Сбоев"}, rows)
	if best >= 0 {
		ui.OK("Самый быстрый публичный: %s (%s)", results[best].name, results[best].ip)
	}
	ui.Info("")
	ui.Info("Измерение идёт по UDP/53; при активном VPN/TUN результаты отражают путь через туннель.")
	if ui.Confirm("Задать DNS на физическом адаптере?", false) {
		setDNS()
	}
}

func physUp() []adapter {
	ads, _ := adapters()
	var r []adapter
	for _, a := range ads {
		if a.Status == "Up" && !a.Virt && !tunnelRe.MatchString(a.Desc+" "+a.Name) {
			r = append(r, a)
		}
	}
	return r
}

func pickAdapter() *adapter {
	l := physUp()
	if len(l) == 0 {
		ui.Warn("Активных физических адаптеров нет.")
		return nil
	}
	for i, a := range l {
		ui.Info("%d) %s — %s", i+1, a.Name, a.Desc)
	}
	s, ok := ui.Ask("Номер адаптера (Enter — отмена):")
	if !ok || s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(l) {
		ui.Warn("Нет такого адаптера.")
		return nil
	}
	return &l[n-1]
}

func setDNS() {
	ui.Header("Смена DNS на адаптере")
	if !admin() {
		return
	}
	a := pickAdapter()
	if a == nil {
		return
	}
	ui.Info("Варианты: 1) Cloudflare 1.1.1.1/1.0.0.1  2) Google 8.8.8.8/8.8.4.4  3) Quad9 9.9.9.9/149.112.112.112  4) Яндекс 77.88.8.8/77.88.8.1  5) Авто (DHCP)")
	s, ok := ui.Ask("Выбор:")
	if !ok {
		return
	}
	sets := map[string]string{"1": "'1.1.1.1','1.0.0.1'", "2": "'8.8.8.8','8.8.4.4'", "3": "'9.9.9.9','149.112.112.112'", "4": "'77.88.8.8','77.88.8.1'"}
	var script string
	switch {
	case sets[s] != "":
		script = fmt.Sprintf(`Set-DnsClientServerAddress -InterfaceIndex %d -ServerAddresses %s; Clear-DnsClientCache`, a.Index, sets[s])
	case s == "5":
		script = fmt.Sprintf(`Set-DnsClientServerAddress -InterfaceIndex %d -ResetServerAddresses; Clear-DnsClientCache`, a.Index)
	default:
		ui.Warn("Отмена.")
		return
	}
	if _, err := win.PS(30*time.Second, script); err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("DNS на «%s» изменён.", a.Name)
}

func restartAdapter() {
	ui.Header("Перезапуск сетевого адаптера")
	if !admin() {
		return
	}
	a := pickAdapter()
	if a == nil {
		return
	}
	if !ui.Confirm(fmt.Sprintf("Перезапустить «%s»? Соединение пропадёт на несколько секунд.", a.Name), false) {
		return
	}
	if _, err := win.PS(60*time.Second, fmt.Sprintf(`Restart-NetAdapter -Name '%s' -Confirm:$false`, strings.ReplaceAll(a.Name, "'", "''"))); err != nil {
		ui.Fail("Не удалось: %v", err)
		return
	}
	ui.OK("Адаптер перезапущен.")
}

func traffic() {
	ui.Header("Трафик и соединения")
	out, _ := win.PS(60*time.Second, `
"--- Принято/отправлено с момента включения адаптеров ---"
Get-NetAdapterStatistics -ErrorAction SilentlyContinue | Where-Object { $_.ReceivedBytes -gt 0 -or $_.SentBytes -gt 0 } | ForEach-Object { "{0,-28} принято {1,10:N1} МБ   отправлено {2,10:N1} МБ" -f $_.Name, ($_.ReceivedBytes/1MB), ($_.SentBytes/1MB) }
"--- Соединения по процессам (топ 12) ---"
$names=@{}; Get-Process | ForEach-Object { $names[$_.Id]=$_.ProcessName }
Get-NetTCPConnection -State Established -ErrorAction SilentlyContinue | Group-Object OwningProcess | Sort-Object Count -Descending | Select-Object -First 12 | ForEach-Object { "{0,-28} {1,4} соединений" -f $names[[int]$_.Name], $_.Count }
"--- Слушающие порты: " + @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue).Count`)
	ui.Info("%s", out)
	ui.Info("")
	ui.Info("Подсказка: аномально много соединений у одного процесса или трафик в простое — повод проверить программу")
	ui.Info("(Диспетчер задач → Производительность → Монитор ресурсов → Сеть).")
}

func fullReset() {
	ui.Header("Полный сброс сети")
	if !admin() {
		return
	}
	ui.Info("Выполнит: сброс DNS-кэша, Winsock, TCP/IP (IPv4+IPv6), обновление DHCP. После — перезагрузка.")
	ui.Warn(vpnWarn)
	if !ui.Confirm("Выполнить полный сброс?", false) {
		return
	}
	win.Stream("ipconfig.exe", "/flushdns")
	win.Stream("netsh.exe", "winsock", "reset")
	win.Stream("netsh.exe", "int", "ip", "reset")
	win.Stream("netsh.exe", "int", "ipv6", "reset")
	win.Stream("ipconfig.exe", "/release")
	win.Stream("ipconfig.exe", "/renew")
	ui.OK("Сброс выполнен.")
	if ui.Confirm("Перезагрузить компьютер сейчас?", false) {
		win.Run(10*time.Second, "shutdown.exe", "/r", "/t", "15", "/c", "WinFix: сброс сети")
	}
}
