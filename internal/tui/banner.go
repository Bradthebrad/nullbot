package tui

import "strings"

const bannerHeight = 7

var retroGlyphs = map[rune][bannerHeight]string{
	'A': {"  XXX  ", " XX XX ", "XX   XX", "XX   XX", "XXXXXXX", "XX   XX", "XX   XX"},
	'B': {"XXXXXX ", "XX   XX", "XX   XX", "XXXXXX ", "XX   XX", "XX   XX", "XXXXXX "},
	'C': {" XXXXX ", "XX   XX", "XX     ", "XX     ", "XX     ", "XX   XX", " XXXXX "},
	'D': {"XXXXXX ", "XX   XX", "XX   XX", "XX   XX", "XX   XX", "XX   XX", "XXXXXX "},
	'E': {"XXXXXXX", "XX     ", "XX     ", "XXXXXX ", "XX     ", "XX     ", "XXXXXXX"},
	'F': {"XXXXXXX", "XX     ", "XX     ", "XXXXXX ", "XX     ", "XX     ", "XX     "},
	'G': {" XXXXX ", "XX   XX", "XX     ", "XX XXXX", "XX   XX", "XX   XX", " XXXXX "},
	'H': {"XX   XX", "XX   XX", "XX   XX", "XXXXXXX", "XX   XX", "XX   XX", "XX   XX"},
	'I': {"XXXXXXX", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "XXXXXXX"},
	'J': {"XXXXXXX", "    XX ", "    XX ", "    XX ", "XX  XX ", "XX  XX ", " XXXX  "},
	'K': {"XX   XX", "XX  XX ", "XX XX  ", "XXXX   ", "XX XX  ", "XX  XX ", "XX   XX"},
	'L': {"XX     ", "XX     ", "XX     ", "XX     ", "XX     ", "XX     ", "XXXXXXX"},
	'M': {"XX   XX", "XXX XXX", "XXXXXXX", "XX X XX", "XX   XX", "XX   XX", "XX   XX"},
	'N': {"XX   XX", "XXX  XX", "XXXX XX", "XX XXXX", "XX  XXX", "XX   XX", "XX   XX"},
	'O': {" XXXXX ", "XX   XX", "XX   XX", "XX   XX", "XX   XX", "XX   XX", " XXXXX "},
	'P': {"XXXXXX ", "XX   XX", "XX   XX", "XXXXXX ", "XX     ", "XX     ", "XX     "},
	'Q': {" XXXXX ", "XX   XX", "XX   XX", "XX   XX", "XX X XX", "XX  XX ", " XXXX X"},
	'R': {"XXXXXX ", "XX   XX", "XX   XX", "XXXXXX ", "XX XX  ", "XX  XX ", "XX   XX"},
	'S': {" XXXXXX", "XX     ", "XX     ", " XXXXX ", "     XX", "     XX", "XXXXXX "},
	'T': {"XXXXXXX", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  "},
	'U': {"XX   XX", "XX   XX", "XX   XX", "XX   XX", "XX   XX", "XX   XX", " XXXXX "},
	'V': {"XX   XX", "XX   XX", "XX   XX", "XX   XX", " XX XX ", " XX XX ", "  XXX  "},
	'W': {"XX   XX", "XX   XX", "XX   XX", "XX X XX", "XXXXXXX", "XXX XXX", "XX   XX"},
	'X': {"XX   XX", "XX   XX", " XX XX ", "  XXX  ", " XX XX ", "XX   XX", "XX   XX"},
	'Y': {"XX   XX", "XX   XX", " XX XX ", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  "},
	'Z': {"XXXXXXX", "    XX ", "   XX  ", "  XX   ", " XX    ", "XX     ", "XXXXXXX"},
	'0': {" XXXXX ", "XX   XX", "XX  XXX", "XX X XX", "XXX  XX", "XX   XX", " XXXXX "},
	'1': {"  XXX  ", " XXXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "  XXX  ", "XXXXXXX"},
	'2': {"XXXXXX ", "     XX", "     XX", " XXXXX ", "XX     ", "XX     ", "XXXXXXX"},
	'3': {"XXXXXX ", "     XX", "     XX", " XXXXX ", "     XX", "     XX", "XXXXXX "},
	'4': {"XX   XX", "XX   XX", "XX   XX", "XXXXXXX", "     XX", "     XX", "     XX"},
	'5': {"XXXXXXX", "XX     ", "XX     ", "XXXXXX ", "     XX", "     XX", "XXXXXX "},
	'6': {" XXXXX ", "XX     ", "XX     ", "XXXXXX ", "XX   XX", "XX   XX", " XXXXX "},
	'7': {"XXXXXXX", "     XX", "    XX ", "   XX  ", "  XX   ", " XX    ", "XX     "},
	'8': {" XXXXX ", "XX   XX", "XX   XX", " XXXXX ", "XX   XX", "XX   XX", " XXXXX "},
	'9': {" XXXXX ", "XX   XX", "XX   XX", " XXXXXX", "     XX", "     XX", " XXXXX "},
	' ': {"    ", "    ", "    ", "    ", "    ", "    ", "    "},
	'-': {"       ", "       ", "       ", "XXXXXXX", "       ", "       ", "       "},
	'_': {"       ", "       ", "       ", "       ", "       ", "       ", "XXXXXXX"},
}

var compactRetroGlyphs = map[rune][bannerHeight]string{
	'A': {" XX ", "X  X", "X  X", "XXXX", "X  X", "X  X", "X  X"},
	'B': {"XXX ", "X  X", "X  X", "XXX ", "X  X", "X  X", "XXX "},
	'C': {" XXX", "X   ", "X   ", "X   ", "X   ", "X   ", " XXX"},
	'D': {"XXX ", "X  X", "X  X", "X  X", "X  X", "X  X", "XXX "},
	'E': {"XXXX", "X   ", "X   ", "XXX ", "X   ", "X   ", "XXXX"},
	'F': {"XXXX", "X   ", "X   ", "XXX ", "X   ", "X   ", "X   "},
	'G': {" XXX", "X   ", "X   ", "X XX", "X  X", "X  X", " XXX"},
	'H': {"X  X", "X  X", "X  X", "XXXX", "X  X", "X  X", "X  X"},
	'I': {"XXXX", " XX ", " XX ", " XX ", " XX ", " XX ", "XXXX"},
	'J': {"XXXX", "  X ", "  X ", "  X ", "  X ", "X X ", " XX "},
	'K': {"X  X", "X X ", "XX  ", "XX  ", "X X ", "X  X", "X  X"},
	'L': {"X   ", "X   ", "X   ", "X   ", "X   ", "X   ", "XXXX"},
	'M': {"X  X", "XXXX", "XXXX", "XXXX", "X  X", "X  X", "X  X"},
	'N': {"X  X", "XX X", "XXXX", "XXXX", "X XX", "X  X", "X  X"},
	'O': {" XX ", "X  X", "X  X", "X  X", "X  X", "X  X", " XX "},
	'P': {"XXX ", "X  X", "X  X", "XXX ", "X   ", "X   ", "X   "},
	'Q': {" XX ", "X  X", "X  X", "X  X", "X XX", "X X ", " XXX"},
	'R': {"XXX ", "X  X", "X  X", "XXX ", "XX  ", "X X ", "X  X"},
	'S': {" XXX", "X   ", "X   ", " XX ", "  X ", "  X ", "XXX "},
	'T': {"XXXX", " XX ", " XX ", " XX ", " XX ", " XX ", " XX "},
	'U': {"X  X", "X  X", "X  X", "X  X", "X  X", "X  X", " XX "},
	'V': {"X  X", "X  X", "X  X", "X  X", "X  X", " XX ", " XX "},
	'W': {"X  X", "X  X", "X  X", "XXXX", "XXXX", "XXXX", "X  X"},
	'X': {"X  X", "X  X", " XX ", " XX ", " XX ", "X  X", "X  X"},
	'Y': {"X  X", "X  X", " XX ", " XX ", " XX ", " XX ", " XX "},
	'Z': {"XXXX", "  X ", "  X ", " X  ", " X  ", "X   ", "XXXX"},
	'0': {" XX ", "X  X", "X XX", "XX X", "X  X", "X  X", " XX "},
	'1': {" XX ", "XXX ", " XX ", " XX ", " XX ", " XX ", "XXXX"},
	'2': {"XXX ", "   X", "   X", " XX ", "X   ", "X   ", "XXXX"},
	'3': {"XXX ", "   X", "   X", " XX ", "   X", "   X", "XXX "},
	'4': {"X  X", "X  X", "X  X", "XXXX", "   X", "   X", "   X"},
	'5': {"XXXX", "X   ", "X   ", "XXX ", "   X", "   X", "XXX "},
	'6': {" XX ", "X   ", "X   ", "XXX ", "X  X", "X  X", " XX "},
	'7': {"XXXX", "   X", "  X ", "  X ", " X  ", " X  ", "X   "},
	'8': {" XX ", "X  X", "X  X", " XX ", "X  X", "X  X", " XX "},
	'9': {" XX ", "X  X", "X  X", " XXX", "   X", "   X", " XX "},
	' ': {"  ", "  ", "  ", "  ", "  ", "  ", "  "},
	'-': {"    ", "    ", "    ", "XXXX", "    ", "    ", "    "},
	'_': {"    ", "    ", "    ", "    ", "    ", "    ", "XXXX"},
}

func blockTitle(text string) string {
	return rawRetroTitle(text, retroGlyphs)
}

func compactBlockTitle(text string) string {
	return rawRetroTitle(text, compactRetroGlyphs)
}

func styledBlockTitle(text string) string {
	return renderRetroTitle(text, retroGlyphs)
}

func styledCompactBlockTitle(text string) string {
	return renderRetroTitle(text, compactRetroGlyphs)
}

func rawRetroTitle(text string, glyphs map[rune][bannerHeight]string) string {
	text = normalizeBannerText(text)
	if text == "" {
		text = "NULLBOT"
	}
	lines := make([]string, bannerHeight)
	for _, r := range text {
		glyph, ok := glyphs[r]
		if !ok {
			glyph = glyphs[' ']
		}
		for i := 0; i < bannerHeight; i++ {
			if lines[i] != "" {
				lines[i] += " "
			}
			lines[i] += glyph[i]
		}
	}
	return strings.Join(lines, "\n")
}

func renderRetroTitle(text string, glyphs map[rune][bannerHeight]string) string {
	raw := rawRetroTitle(text, glyphs)
	source := strings.Split(raw, "\n")
	width := maxRuneWidth(source) + 2
	height := len(source) + 1
	shadow := make([][]bool, height)
	fill := make([][]bool, height)
	for i := range shadow {
		shadow[i] = make([]bool, width)
		fill[i] = make([]bool, width)
	}
	for y, line := range source {
		for x, r := range []rune(line) {
			if r == ' ' {
				continue
			}
			fill[y][x] = true
			if y+1 < height && x+1 < width && isRetroEdge(source, x, y) {
				shadow[y+1][x+1] = true
			}
		}
	}

	var out []string
	for y := 0; y < height; y++ {
		var b strings.Builder
		for x := 0; x < width; x++ {
			switch {
			case fill[y][x]:
				if y <= 1 {
					b.WriteString(bannerTopStyle.Render("█"))
				} else if y >= bannerHeight-2 {
					b.WriteString(bannerFaceLowStyle.Render("█"))
				} else {
					b.WriteString(bannerFaceHighStyle.Render("█"))
				}
			case shadow[y][x]:
				b.WriteString(bannerOutlineStyle.Render("░"))
			default:
				b.WriteByte(' ')
			}
		}
		out = append(out, b.String())
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func isRetroEdge(source []string, x int, y int) bool {
	for _, offset := range [][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}} {
		ny := y + offset[1]
		nx := x + offset[0]
		if ny < 0 || ny >= len(source) {
			return true
		}
		row := []rune(source[ny])
		if nx < 0 || nx >= len(row) || row[nx] == ' ' {
			return true
		}
	}
	return false
}

func maxRuneWidth(lines []string) int {
	width := 0
	for _, line := range lines {
		if n := len([]rune(line)); n > width {
			width = n
		}
	}
	return width
}

func normalizeBannerText(text string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(text)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
