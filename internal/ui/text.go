package ui

import (
	"fmt"
	"strings"
)

// FormatBytes переводит байты в удобочитаемый вид.
func FormatBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case n < 1024:
		return fmt.Sprintf("%d Б", n)
	case f < k*k:
		return fmt.Sprintf("%.1f КБ", f/k)
	case f < k*k*k:
		return fmt.Sprintf("%.1f МБ", f/(k*k))
	default:
		return fmt.Sprintf("%.2f ГБ", f/(k*k*k))
	}
}

// Trunc обрезает строку до n рун.
func Trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Wrap переносит текст по словам на строки не длиннее width рун.
func Wrap(s string, width int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		cur := ""
		for _, w := range strings.Fields(para) {
			switch {
			case cur == "":
				cur = w
			case len([]rune(cur))+1+len([]rune(w)) <= width:
				cur += " " + w
			default:
				lines = append(lines, cur)
				cur = w
			}
		}
		lines = append(lines, cur)
	}
	return lines
}
