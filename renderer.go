package main

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
)

type color string

var colorScheme = map[string]string{
	"brightgreen": "#4c1",
	"green":       "#97ca00",
	"yellow":      "#dfb317",
	"yellowgreen": "#a4a61d",
	"orange":      "#fe7d37",
	"red":         "#e05d44",
	"blue":        "#007ec6",
	"grey":        "#555",
	"gray":        "#555",
	"lightgrey":   "#9f9f9f",
	"lightgray":   "#9f9f9f",
}

const (
	colorBrightgreen = color("brightgreen")
	colorGreen       = color("green")
	colorYellow      = color("yellow")
	colorYellowgreen = color("yellowgreen")
	colorOrange      = color("orange")
	colorRed         = color("red")
	colorBlue        = color("blue")
	colorGrey        = color("grey")
	colorGray        = color("gray")
	colorLightgrey   = color("lightgrey")
	colorLightgray   = color("lightgray")
)

func (c color) String() string {
	color, ok := colorScheme[string(c)]
	if ok {
		return color
	}
	return string(c)
}

const (
	fontsize = 11
	dpi      = 72
)

var drawer *badgeDrawer

func mustNewFontDrawer(size, dpi float64) *font.Drawer {
	ttf, err := truetype.Parse(veraSans)
	if err != nil {
		panic(err)
	}
	return &font.Drawer{
		Face: truetype.NewFace(ttf, &truetype.Options{
			Size:    size,
			DPI:     dpi,
			Hinting: font.HintingFull,
		}),
	}
}

type badge struct {
	Subject string
	Status  string
	Color   color
	Bounds  bounds
}

type bounds struct {
	// SubjectDx is the width of subject string of the badge.
	SubjectDx float64
	SubjectX  float64
	// StatusDx is the width of status string of the badge.
	StatusDx float64
	StatusX  float64
}

func (b bounds) Dx() float64 {
	return b.SubjectDx + b.StatusDx
}

type badgeDrawer struct {
	fd    *font.Drawer
	tmpl  *template.Template
	mutex *sync.Mutex
}

func (d *badgeDrawer) Render(subject, status string, color color, w io.Writer) error {
	d.mutex.Lock()
	subjectDx := d.measureString(subject)
	statusDx := d.measureString(status)
	d.mutex.Unlock()

	bdg := badge{
		Subject: subject,
		Status:  status,
		Color:   color,
		Bounds: bounds{
			SubjectDx: subjectDx,
			SubjectX:  subjectDx/2.0 + 1,
			StatusDx:  statusDx,
			StatusX:   subjectDx + statusDx/2.0 - 1,
		},
	}
	return d.tmpl.Execute(w, bdg)
}

func (d *badgeDrawer) RenderBytes(subject, status string, color color) ([]byte, error) {
	buf := &bytes.Buffer{}
	err := d.Render(subject, status, color, buf)
	return buf.Bytes(), err
}

func (d *badgeDrawer) measureString(s string) float64 {
	sm := d.fd.MeasureString(s)
	// this 64 is weird but it's the way I've found how to convert fixed.Int26_6 to float64
	return float64(sm)/64 + extraDx
}

// shield.io uses Verdana.ttf to measure text width with an extra 10px.
// As we use Vera.ttf, we have to tune this value a little.
const extraDx = 13

var veraSans []byte

func init() {
	var err error
	veraSans, err = base64.StdEncoding.DecodeString(veraSansBase64)
	if err != nil {
		log.Fatalf("Couldn't decode base64 font data: %s\n", err)
	}

	drawer = &badgeDrawer{
		fd:    mustNewFontDrawer(fontsize, dpi),
		tmpl:  template.Must(template.New("flat-template").Parse(flatTemplate)),
		mutex: &sync.Mutex{},
	}
}

var flatTemplate = strings.TrimSpace(`
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="{{.Bounds.Dx}}" height="20">
  <linearGradient id="smooth" x2="0" y2="100%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <mask id="round">
    <rect width="{{.Bounds.Dx}}" height="20" rx="3" fill="#fff"/>
  </mask>
  <g mask="url(#round)">
    <rect width="{{.Bounds.SubjectDx}}" height="20" fill="#555"/>
    <rect x="{{.Bounds.SubjectDx}}" width="{{.Bounds.StatusDx}}" height="20" fill="{{or .Color "#4c1" | html}}"/>
    <rect width="{{.Bounds.Dx}}" height="20" fill="url(#smooth)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
    <text x="{{.Bounds.SubjectX}}" y="15" fill="#010101" fill-opacity=".3">{{.Subject | html}}</text>
    <text x="{{.Bounds.SubjectX}}" y="14">{{.Subject | html}}</text>
    <text x="{{.Bounds.StatusX}}" y="15" fill="#010101" fill-opacity=".3">{{.Status | html}}</text>
    <text x="{{.Bounds.StatusX}}" y="14">{{.Status | html}}</text>
  </g>
</svg>
`)
