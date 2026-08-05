package pdf

import (
	"strconv"
	"strings"
)

// winAnsi maps a byte to its Unicode rune under WinAnsiEncoding (Windows-1252).
// 0 means undefined.
var winAnsi [256]rune

// cp1252High holds the 0x80–0x9F Windows-1252 specials that differ from Latin-1.
var cp1252High = map[byte]rune{
	0x80: 0x20AC, 0x82: 0x201A, 0x83: 0x0192, 0x84: 0x201E, 0x85: 0x2026,
	0x86: 0x2020, 0x87: 0x2021, 0x88: 0x02C6, 0x89: 0x2030, 0x8A: 0x0160,
	0x8B: 0x2039, 0x8C: 0x0152, 0x8E: 0x017D, 0x91: 0x2018, 0x92: 0x2019,
	0x93: 0x201C, 0x94: 0x201D, 0x95: 0x2022, 0x96: 0x2013, 0x97: 0x2014,
	0x98: 0x02DC, 0x99: 0x2122, 0x9A: 0x0161, 0x9B: 0x203A, 0x9C: 0x0153,
	0x9E: 0x017E, 0x9F: 0x0178,
}

func init() {
	for c := 0x20; c <= 0x7E; c++ {
		winAnsi[c] = rune(c)
	}
	for c := 0xA0; c <= 0xFF; c++ {
		winAnsi[c] = rune(c) // Latin-1 == Unicode in this range
	}
	for c, r := range cp1252High {
		winAnsi[c] = r
	}
}

// glyphNames maps common PostScript glyph names to runes, covering the entries
// most likely to appear in an /Encoding /Differences array. Names of the form
// "uniXXXX" and "uXXXXXX" are handled programmatically in glyphToRune.
var glyphNames = map[string]rune{
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#',
	"dollar": '$', "percent": '%', "ampersand": '&', "quotesingle": '\'',
	"parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+',
	"comma": ',', "hyphen": '-', "period": '.', "slash": '/',
	"zero": '0', "one": '1', "two": '2', "three": '3', "four": '4',
	"five": '5', "six": '6', "seven": '7', "eight": '8', "nine": '9',
	"colon": ':', "semicolon": ';', "less": '<', "equal": '=', "greater": '>',
	"question": '?', "at": '@', "bracketleft": '[', "backslash": '\\',
	"bracketright": ']', "asciicircum": '^', "underscore": '_', "grave": '`',
	"braceleft": '{', "bar": '|', "braceright": '}', "asciitilde": '~',
	"quoteleft": '\u2018', "quoteright": '\u2019', "quotedblleft": '\u201C',
	"quotedblright": '\u201D', "bullet": '\u2022', "endash": '\u2013',
	"emdash": '\u2014', "ellipsis": '\u2026', "trademark": '\u2122',
	"fi": '\uFB01', "fl": '\uFB02', "florin": '\u0192', "dagger": '\u2020',
	"daggerdbl": '\u2021', "degree": '\u00B0', "bulletoperator": '\u2219',
	"minus": '\u2212', "periodcentered": '\u00B7', "nbspace": '\u00A0',
}

// glyphToRune resolves a PostScript glyph name to a rune (0 if unknown).
func glyphToRune(name string) rune {
	if r, ok := glyphNames[name]; ok {
		return r
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		if v, err := strconv.ParseInt(name[3:7], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 {
		if v, err := strconv.ParseInt(name[1:], 16, 32); err == nil {
			return rune(v)
		}
	}
	// "gNN"/"cidNN"/"CNN" style names carry no Unicode; give up.
	return 0
}
