//go:build windows

package win

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

var (
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procIsDBCSLeadByteEx    = kernel32.NewProc("IsDBCSLeadByteEx")
)

const cpOEM = 1 // CP_OEMCP

const (
	encUnknown = iota
	encUTF8
	encOEM
	encUTF16
)

// Decoder декодирует вывод консольных программ потоком:
// UTF-16LE (sfc), UTF-8 (PowerShell) или OEM-кодировка системы (DISM, netsh, ipconfig).
type Decoder struct {
	mode  int
	carry []byte
}

// Write принимает очередной кусок байтов и возвращает декодированный текст.
func (d *Decoder) Write(b []byte) string {
	if len(b) == 0 && len(d.carry) == 0 {
		return ""
	}
	data := append(d.carry, b...)
	d.carry = nil
	if d.mode == encUnknown {
		zeros, high := 0, false
		for _, c := range data {
			if c == 0 {
				zeros++
			}
			if c >= 0x80 {
				high = true
			}
		}
		switch {
		case len(data) >= 2 && zeros*4 >= len(data):
			d.mode = encUTF16
		case high && utf8.Valid(data):
			d.mode = encUTF8
		case high:
			// возможно, обрезанный UTF-8 в конце
			t := data
			for i := 0; i < 3 && len(t) > 0 && !utf8.Valid(t); i++ {
				t = t[:len(t)-1]
			}
			if utf8.Valid(t) {
				d.mode = encUTF8
			} else {
				d.mode = encOEM
			}
		}
	}
	switch d.mode {
	case encUTF16:
		if len(data)%2 == 1 {
			d.carry = []byte{data[len(data)-1]}
			data = data[:len(data)-1]
		}
		u := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			if c := uint16(data[i]) | uint16(data[i+1])<<8; c != 0xFEFF {
				u = append(u, c)
			}
		}
		return string(utf16.Decode(u))
	case encOEM:
		// не режем двухбайтовый символ (CJK-кодировки) посередине
		if n := len(data); n > 0 {
			if r, _, _ := procIsDBCSLeadByteEx.Call(cpOEM, uintptr(data[n-1])); r != 0 {
				d.carry = []byte{data[n-1]}
				data = data[:n-1]
			}
		}
		return oemToString(data)
	default: // UTF-8 / ASCII
		cut := len(data)
		for i := 1; i <= 3 && i <= len(data); i++ {
			if data[len(data)-i] >= 0xC0 { // начало многобайтовой руны
				if !utf8.FullRune(data[len(data)-i:]) {
					cut = len(data) - i
				}
				break
			}
		}
		d.carry = append([]byte(nil), data[cut:]...)
		return string(data[:cut])
	}
}

// oemToString переводит текст из OEM-кодовой страницы системы
// (cp866 в русской Windows, cp437/850 в других) через MultiByteToWideChar.
func oemToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	n, _, _ := procMultiByteToWideChar.Call(cpOEM, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	procMultiByteToWideChar.Call(cpOEM, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&buf[0])), n)
	return string(utf16.Decode(buf))
}

// DecodeOutput декодирует полный буфер вывода.
func DecodeOutput(b []byte) string {
	var d Decoder
	s := d.Write(b)
	if len(d.carry) > 0 {
		s += string(d.carry)
	}
	return strings.ReplaceAll(s, "\x00", "")
}
