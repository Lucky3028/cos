package domain

import "testing"

func TestSanitizeTerminalTextRemovesTerminalControls(t *testing.T) {
	value := "\x1b[31mred\x1b[0m\nnext\r\t\x7f\u0080"

	if got := SanitizeTerminalText(value, true); got != "red\nnext" {
		t.Fatalf("preserve newlines = %q, want %q", got, "red\nnext")
	}
	if got := SanitizeTerminalText(value, false); got != "rednext" {
		t.Fatalf("single line = %q, want %q", got, "rednext")
	}
}

func TestSanitizeTerminalTextPreservesPrintableUnicode(t *testing.T) {
	const value = "日本語 🙂 café"
	if got := SanitizeTerminalText(value, true); got != value {
		t.Fatalf("sanitized printable text = %q, want %q", got, value)
	}
}

func TestSanitizeSingleLineNormalizesWhitespace(t *testing.T) {
	value := "  one\n\t two \x1b[31mthree\x1b[0m  "
	if got := SanitizeSingleLine(value); got != "one two three" {
		t.Fatalf("single line = %q, want %q", got, "one two three")
	}
}
