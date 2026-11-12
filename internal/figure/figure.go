// Package figure draws the figure the README opens with: the proxyfile a person writes beside the whole configuration
// proxyfile generates from it. Both panes are drawn from the two files' own text, so a test keeps the committed figure
// equal to what the program writes today.
//
// The figure is plain SVG 1.1 that carries its text, colours and fonts as presentation attributes. A README shows it as
// an image, which runs no script and loads no other file, and the reader's browser draws its text with the fonts the
// reader's system has, so the figure names a list of common ones and never a font file.
package figure

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Theme is the colour scheme a figure is drawn in. The README shows the one that matches the reader's GitHub theme.
type Theme int

const (
	Light Theme = iota
	Dark
)

type palette struct{ page, pane, border, text, muted, accent string }

var palettes = [...]palette{
	Light: {page: "#ffffff", pane: "#f6f8fa", border: "#d0d7de", text: "#1f2328", muted: "#656d76", accent: "#5b4bd6"},
	Dark:  {page: "#0d1117", pane: "#161b22", border: "#30363d", text: "#e6edf3", muted: "#8d96a0", accent: "#9d8cff"},
}

// sourceShare is the left pane's share of the panes' width, in percent. At 40 a row holds 53 glyphs of sourceSize, and
// a longer source line wraps.
const sourceShare = 40

// The layout, in pixels.
const (
	width          = 1280
	margin         = 24 // around the two panes
	gap            = 24 // between them
	sourceWidth    = (width - 2*margin - gap) * sourceShare / 100
	generatedWidth = width - 2*margin - gap - sourceWidth
	radius         = 12
	titleHeight    = 44 // the title bar at the top of each pane
	titleSize      = 15
	padding        = 16 // between a pane's edges and its text
	gutterGap      = 12 // between a line's number and its text
	sourceSize     = 13
	sourceLine     = 20
	generatedSize  = 8
	generatedLine  = 11
	columnGap      = 16
	// generatedMaxHeight is the tallest the right pane may be: past it the generated lines take another column.
	generatedMaxHeight = 760
)

const (
	// glyphWidth is the advance of a monospace glyph, in em, rounded up from the widest the figure's fonts give: SF
	// Mono, which Safari draws ui-monospace with, advances 0.618 em, and Menlo, which Chrome falls back to, and DejaVu
	// Sans Mono 0.602. A wrapped row then stays inside the left pane whichever of them the reader's system has.
	glyphWidth = 0.62
	// capHeight is the height of a capital letter, in em, by which a row of text is centred on its line.
	capHeight = 0.7
)

// preserve keeps a line's spaces, its indent above all. Chromium and WebKit honour it only on the <text> element
// itself, not inherited from the <svg> or a <g> as Firefox does, so every line's element carries it.
const preserve = `xml:space="preserve"`

const (
	sansFamily = `-apple-system, BlinkMacSystemFont, &quot;Segoe UI&quot;, Helvetica, Arial, sans-serif`
	monoFamily = `ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`
)

// Comparison returns an SVG document that shows every line of source, the file a person writes, in a readable pane
// beside every line of generated, the file proxyfile generates from it, in a wider pane whose text is too small to
// read: the figure shows how much is generated, not what. The names are the files' names shown in the titles.
func Comparison(source, generated []byte, sourceName, generatedName string, theme Theme) []byte {
	colours := palettes[theme]
	sourceLines, generatedLines := lines(source), lines(generated)
	left := sourceLayout(sourceLines)
	right := generatedLayout(len(generatedLines))
	body := max(left.height, right.height)
	height := 2*margin + body
	// The shorter pane is centred on the taller one, so the arrow at mid height points from the middle of one to the
	// middle of the other.
	leftTop := margin + (body-left.height)/2
	rightTop := margin + (body-right.height)/2
	rightX := margin + sourceWidth + gap

	var out svg
	out.printf(`<svg xmlns="http://www.w3.org/2000/svg" version="1.1" width="%d" height="%d" viewBox="0 0 %d %d" role="img">`+"\n",
		width, height, width, height)
	out.printf("<title>A %d-line proxyfile beside the %d-line Envoy configuration proxyfile generates from it</title>\n",
		len(sourceLines), len(generatedLines))
	out.printf(`<rect width="%d" height="%d" fill="%s"/>`+"\n", width, height, colours.page)

	out.printf(`<g id="source">` + "\n")
	out.pane(margin, leftTop, sourceWidth, left.height, colours.accent, colours)
	out.title(margin, leftTop, "You write: "+sourceName+", "+count(len(sourceLines)), colours)
	out.sourceText(sourceLines, left, margin, leftTop+titleHeight+padding, colours)
	out.printf("</g>\n")

	arrowX, arrowY := margin+sourceWidth+gap/2, margin+body/2
	out.printf(`<path d="M%d %dh11" stroke="%s" stroke-width="2"/>`+"\n", arrowX-9, arrowY, colours.accent)
	out.printf(`<path d="M%d %dl8 6l-8 6z" fill="%s"/>`+"\n", arrowX+1, arrowY-6, colours.accent)

	out.printf(`<g id="generated">` + "\n")
	out.pane(rightX, rightTop, generatedWidth, right.height, colours.border, colours)
	out.title(rightX, rightTop, "proxyfile generates: "+generatedName+", "+count(len(generatedLines)), colours)
	out.generatedText(generatedLines, right, rightX, rightTop+titleHeight+padding, colours)
	out.printf("</g>\n")
	out.printf("</svg>\n")
	return []byte(out.String())
}

// lines returns the lines of text, the last one whether or not a newline ends it, and none for an empty file.
func lines(text []byte) []string {
	if len(text) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
}

// count writes n as a number of lines, for a title.
func count(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

// sourcePane is where the left pane draws its text: a gutter with each line's number, then the line, on as many rows as
// the line needs at the pane's width.
type sourcePane struct {
	numberX, textX float64    // the number ends at numberX, the text starts at textX, both from the pane's left edge
	hangs          []int      // by line, the glyphs a row after a line's first starts to the right of textX
	rows           [][]string // by line, the rows it is drawn on
	height         int
}

// sourceLayout wraps each line that is wider than the left pane, so no line runs past the pane's edge at the size the
// pane is read at. A row after a line's first hangs two glyphs right of the line's indent: past "# " in a comment.
func sourceLayout(sourceLines []string) sourcePane {
	glyph := glyphWidth * sourceSize
	digits := len(strconv.Itoa(len(sourceLines)))
	layout := sourcePane{numberX: padding + float64(digits)*glyph}
	layout.textX = layout.numberX + gutterGap
	glyphs := int((sourceWidth - padding - layout.textX) / glyph)
	rows := 0
	for _, line := range sourceLines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		hang := min(indent+2, glyphs/2)
		wrapped := wrap(line, glyphs, glyphs-hang)
		layout.hangs = append(layout.hangs, hang)
		layout.rows = append(layout.rows, wrapped)
		rows += len(wrapped)
	}
	layout.height = titleHeight + 2*padding + rows*sourceLine
	return layout
}

// generatedPane is where the right pane draws the generated lines: in columns of equal width, each clipped to its width.
type generatedPane struct {
	columns, rows int // rows in a column
	columnWidth   float64
	height        int
}

func generatedLayout(lines int) generatedPane {
	layout := generatedPane{columns: columns(lines)}
	layout.rows = (lines + layout.columns - 1) / layout.columns
	layout.columnWidth = float64(generatedWidth-2*padding-(layout.columns-1)*columnGap) / float64(layout.columns)
	layout.height = titleHeight + 2*padding + layout.rows*generatedLine
	return layout
}

// columns returns how many columns the right pane draws lines in: the fewest that keep it no taller than
// generatedMaxHeight.
func columns(lines int) int {
	perColumn := (generatedMaxHeight - titleHeight - 2*padding) / generatedLine
	return max(1, (lines+perColumn-1)/perColumn)
}

// wrap splits line into the rows the left pane draws it on, the first at most first glyphs wide and each other at
// most rest, so that the rows joined give the line back. A row ends after its last space, or else after its last
// slash, or else at the width; the spaces it ends after stay on it, where they draw as nothing.
func wrap(line string, first, rest int) []string {
	runes := []rune(line)
	indent := len(runes) - len([]rune(strings.TrimLeft(line, " ")))
	var rows []string
	for width := first; len([]rune(strings.TrimRight(string(runes), " "))) > width; width = rest {
		cut := breakAfter(runes, width, indent)
		rows = append(rows, string(runes[:cut]))
		runes = runes[cut:]
		indent = 0
	}
	return append(rows, string(runes))
}

// breakAfter returns how many of runes the row at most width glyphs wide takes. It never ends the row inside the
// line's indent, which would leave a row with nothing on it.
func breakAfter(runes []rune, width, indent int) int {
	// A space right after the width can end the row too, since it draws as nothing.
	for cut := min(width+1, len(runes)); cut > indent; cut-- {
		if runes[cut-1] == ' ' {
			for cut < len(runes) && runes[cut] == ' ' {
				cut++
			}
			return cut
		}
	}
	for cut := width; cut > indent; cut-- {
		if runes[cut-1] == '/' {
			return cut
		}
	}
	return width
}

type svg struct{ strings.Builder }

func (s *svg) printf(format string, args ...any) {
	fmt.Fprintf(&s.Builder, format, args...)
}

// pane draws a pane's rounded box and the line under its title bar, half a pixel in so each 1px line is sharp.
func (s *svg) pane(x, y, w, h int, border string, colours palette) {
	s.printf(`<rect x="%d.5" y="%d.5" width="%d" height="%d" rx="%d" fill="%s" stroke="%s"/>`+"\n",
		x, y, w-1, h-1, radius, colours.pane, border)
	s.printf(`<path d="M%d %d.5h%d" stroke="%s"/>`+"\n", x+1, y+titleHeight, w-2, border)
}

func (s *svg) title(x, y int, title string, colours palette) {
	s.printf(`<text x="%d" y="%s" font-family="%s" font-size="%d" font-weight="600" fill="%s">%s</text>`+"\n",
		x+padding, px(float64(y)+baseline(titleHeight, titleSize)), sansFamily, titleSize, colours.text, escape(title))
}

func (s *svg) sourceText(sourceLines []string, layout sourcePane, x, top int, colours palette) {
	textX := float64(x) + layout.textX
	y := func(row int) string { return px(float64(top+row*sourceLine) + baseline(sourceLine, sourceSize)) }
	s.printf(`<g font-family="%s" font-size="%d" fill="%s">`+"\n", monoFamily, sourceSize, colours.text)
	row := 0
	for i, line := range sourceLines {
		s.printf(`<text x="%s" y="%s" fill="%s" text-anchor="end">%d</text>`+"\n",
			px(float64(x)+layout.numberX), y(row), colours.muted, i+1)
		rows := layout.rows[i]
		if len(rows) == 1 {
			s.printf(`<text x="%s" y="%s"%s %s>%s</text>`+"\n", px(textX), y(row), comment(line, colours), preserve,
				escape(line))
			row++
			continue
		}
		// A wrapped line stays one element with a tspan per row, so it still reads back as the one line it is.
		s.printf(`<text%s %s>`, comment(line, colours), preserve)
		for j, text := range rows {
			rowX := textX
			if j > 0 {
				rowX += float64(layout.hangs[i]) * glyphWidth * sourceSize
			}
			s.printf(`<tspan x="%s" y="%s">%s</tspan>`, px(rowX), y(row), escape(text))
			row++
		}
		s.printf("</text>\n")
	}
	s.printf("</g>\n")
}

// generatedText draws the generated lines top to bottom, then left to right, each column clipped to its width, so a
// line longer than the column ends at its edge instead of running over the next column. A thin line between two
// columns shows where one ends.
func (s *svg) generatedText(generatedLines []string, layout generatedPane, x, top int, colours palette) {
	columnX := func(column int) float64 {
		return float64(x+padding) + float64(column)*(layout.columnWidth+columnGap)
	}
	bodyTop, bodyHeight := top-padding, layout.rows*generatedLine+2*padding
	s.printf("<defs>\n")
	for column := range layout.columns {
		s.printf(`<clipPath id="column-%d"><rect x="%s" y="%d" width="%s" height="%d"/></clipPath>`+"\n",
			column+1, px(columnX(column)), bodyTop, px(layout.columnWidth), bodyHeight)
	}
	s.printf("</defs>\n")
	for column := 1; column < layout.columns; column++ {
		s.printf(`<path d="M%s %dv%d" stroke="%s"/>`+"\n",
			px(math.Floor(columnX(column)-columnGap/2)+0.5), top, layout.rows*generatedLine, colours.border)
	}
	s.printf(`<g font-family="%s" font-size="%d" fill="%s">`+"\n", monoFamily, generatedSize, colours.text)
	for column := range layout.columns {
		s.printf(`<g clip-path="url(#column-%d)">`+"\n", column+1)
		first := column * layout.rows
		for row, line := range generatedLines[first:min(first+layout.rows, len(generatedLines))] {
			s.printf(`<text x="%s" y="%s"%s %s>%s</text>`+"\n", px(columnX(column)),
				px(float64(top+row*generatedLine)+baseline(generatedLine, generatedSize)), comment(line, colours), preserve,
				escape(line))
		}
		s.printf("</g>\n")
	}
	s.printf("</g>\n")
}

// comment returns the fill attribute that draws a comment line in the muted colour, or "" for any other line, which
// takes its group's colour.
func comment(line string, colours palette) string {
	if strings.HasPrefix(strings.TrimLeft(line, " "), "#") {
		return ` fill="` + colours.muted + `"`
	}
	return ""
}

// baseline returns where, below the top of a line lineHeight high, a row of text size high sits so that its capital
// letters are centred on the line.
func baseline(lineHeight, size int) float64 {
	return (float64(lineHeight) + capHeight*float64(size)) / 2
}

// px writes a position with at most two decimals, the same for the same value on every machine.
func px(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func escape(text string) string {
	var out strings.Builder
	// EscapeText fails only when the writer does, and a strings.Builder never fails a write.
	_ = xml.EscapeText(&out, []byte(text))
	return out.String()
}
