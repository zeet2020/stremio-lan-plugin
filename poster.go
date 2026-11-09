package main

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Fallback posters: titles with no server poster and no thumbnail (thumbnails
// off, not made yet, or failed) get a drawn one instead of Stremio's blank
// tile: the title on a colour picked from it, and a small label (year, Series,
// Album...). PNG, not SVG, so every Stremio app can show it.

// posterSizes are the pixel sizes of Stremio's poster shapes.
var posterSizes = map[string][2]int{"poster": {400, 600}, "square": {500, 500}, "landscape": {640, 360}}

// fallbackURL is the drawn poster for title, in a Stremio poster shape.
func fallbackURL(base, shape, title, label string) string {
	return base + "/poster.png?" + url.Values{"s": {shape}, "t": {title}, "l": {label}}.Encode()
}

func handlePoster(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, err := drawPoster(q.Get("s"), q.Get("t"), q.Get("l"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "max-age=604800") // same URL, same picture
	w.Header().Set("Content-Type", "image/png")
	w.Write(b)
}

var (
	fontsOnce        sync.Once
	boldFont, regFnt *opentype.Font
	fontErr          error
)

// drawPoster renders a PNG: a two-tone background coloured by the title, the
// title word-wrapped and centred (smaller when long), the label at the bottom.
// ponytail: drawn on every request (~10 ms); clients cache it a week.
func drawPoster(shape, title, label string) ([]byte, error) {
	fontsOnce.Do(func() {
		if boldFont, fontErr = opentype.Parse(gobold.TTF); fontErr == nil {
			regFnt, fontErr = opentype.Parse(goregular.TTF)
		}
	})
	if fontErr != nil {
		return nil, fontErr
	}
	size, ok := posterSizes[shape]
	if !ok {
		size = posterSizes["poster"]
	}
	w, h := size[0], size[1]
	title = clip(strings.TrimSpace(title), 120)
	label = clip(strings.ToUpper(strings.TrimSpace(label)), 40)

	hf := fnv.New32a()
	hf.Write([]byte(strings.ToLower(title)))
	hue := float64(hf.Sum32() % 360)
	top, bottom := hsl(hue, 0.45, 0.30), hsl(hue, 0.50, 0.11)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c := mix(top, bottom, float64(y)/float64(h-1))
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}

	// title: largest size whose wrapped lines fit the box
	pad := w / 10
	boxW, boxH := w-2*pad, h*3/5
	var face font.Face
	var lines []string
	for pt := float64(w) / 8; ; pt -= 2 {
		f, err := opentype.NewFace(boldFont, &opentype.FaceOptions{Size: pt, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		lines = wrap(f, title, boxW)
		lineH := f.Metrics().Height.Ceil()
		if len(lines)*lineH <= boxH || pt <= 16 {
			if maxLines := max(boxH/lineH, 1); len(lines) > maxLines { // too long even small: cut the rest
				lines = append(lines[:maxLines-1], ellipsize(f, strings.Join(lines[maxLines-1:], " "), boxW))
			}
			face = f
			break
		}
		f.Close()
	}
	defer face.Close()
	m := face.Metrics()
	lineH := m.Height.Ceil()
	y := (h-len(lines)*lineH)/2 + m.Ascent.Ceil() - h/20 // a little above centre
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face}
	for _, ln := range lines {
		d.Dot = fixed.P((w-d.MeasureString(ln).Ceil())/2, y)
		d.DrawString(ln)
		y += lineH
	}

	if label != "" {
		lf, err := opentype.NewFace(regFnt, &opentype.FaceOptions{Size: float64(w) / 22, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		defer lf.Close()
		ld := &font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{255, 255, 255, 170}), Face: lf}
		label = ellipsize(lf, label, boxW)
		ld.Dot = fixed.P((w-ld.MeasureString(label).Ceil())/2, h-h/12)
		ld.DrawString(label)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wrap breaks s into lines no wider than max; a word wider than max gets a
// line to itself (ellipsized).
func wrap(f font.Face, s string, max int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		next := strings.TrimSpace(cur + " " + word)
		if cur != "" && font.MeasureString(f, next).Ceil() > max {
			lines = append(lines, cur)
			next = word
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	for i, ln := range lines {
		lines[i] = ellipsize(f, ln, max)
	}
	return lines
}

// ellipsize shortens s with "…" until it fits max pixels.
func ellipsize(f font.Face, s string, max int) string {
	if font.MeasureString(f, s).Ceil() <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && font.MeasureString(f, string(r)+"…").Ceil() > max {
		r = r[:len(r)-1]
	}
	return strings.TrimSpace(string(r)) + "…"
}

// clip limits s to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

// hsl converts hue (degrees), saturation and lightness (0–1) to RGB.
func hsl(h, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	to := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return color.RGBA{to(r), to(g), to(b), 255}
}
