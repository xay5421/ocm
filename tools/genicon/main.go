// Command genicon renders the ocm logo (see assets/ocm.svg) into a
// multi-size Windows ICO file and a macOS ICNS file using only the standard
// library. The ICO feeds the .exe resource icon (see winres/); the ICNS
// goes into the mac app bundle (see tools/makeapp).
//
// Usage: go run ./tools/genicon [output.ico [output.icns]]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The logo lives on a 64x64 canvas (matching assets/ocm.svg). We render a
// 1536x1536 master (scale 24) with per-pixel geometry, then box-downscale
// to each target size (1536 is divisible by every size below).
const (
	canvas = 64
	scale  = 24
	master = canvas * scale
)

var icoSizes = []int{16, 24, 32, 48, 64, 128, 256}

// icnsEntries maps ICNS chunk types to pixel sizes (PNG payloads are valid
// in all of these since macOS 10.7; the @2x types just carry larger pixels).
var icnsEntries = []struct {
	kind string
	size int
}{
	{"icp4", 16}, {"icp5", 32}, {"icp6", 64},
	{"ic07", 128}, {"ic08", 256}, {"ic09", 512},
	{"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512},
}

var (
	bgFill     = color.NRGBA{0x17, 0x1a, 0x21, 0xff} // panel
	bgStroke   = color.NRGBA{0x2a, 0x2f, 0x3d, 0xff} // border
	tunnelBlue = color.NRGBA{0x6e, 0xa8, 0xfe, 0xff} // accent
	nodeGreen  = color.NRGBA{0x3f, 0xb9, 0x50, 0xff} // ok
	ringDark   = color.NRGBA{0x0f, 0x11, 0x15, 0xff} // bg
)

func main() {
	icoOut := "assets/ocm.ico"
	icnsOut := "assets/ocm.icns"
	if len(os.Args) > 1 {
		icoOut = os.Args[1]
	}
	if len(os.Args) > 2 {
		icnsOut = os.Args[2]
	}
	img := render()

	ico, err := encodeICO(img)
	if err == nil {
		err = os.WriteFile(icoOut, ico, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	fmt.Printf("genicon: wrote %s (%d bytes, sizes %v)\n", icoOut, len(ico), icoSizes)

	icns, err := encodeICNS(img)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(icnsOut), 0o755)
	}
	if err == nil {
		err = os.WriteFile(icnsOut, icns, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	fmt.Printf("genicon: wrote %s (%d bytes, %d entries)\n", icnsOut, len(icns), len(icnsEntries))
}

// render draws the logo on the master canvas. Coordinates mirror ocm.svg.
func render() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, master, master))

	// Tunnel: cubic bezier M20 44 C 20 30, 44 34, 44 20, stroke width 5.
	bez := flattenBezier(
		pt{20, 44}, pt{20, 30}, pt{44, 34}, pt{44, 20}, 256)

	for y := 0; y < master; y++ {
		for x := 0; x < master; x++ {
			// Pixel center in canvas (svg) units.
			px := (float64(x) + 0.5) / scale
			py := (float64(y) + 0.5) / scale

			var c color.NRGBA
			set := false

			// Rounded rect x=2 y=2 w=60 h=60 rx=14, stroke width 2.
			d := sdRoundRect(px, py, 32, 32, 30, 30, 14)
			switch {
			case d > 1: // outside stroke: transparent
				img.SetNRGBA(x, y, color.NRGBA{})
				continue
			case d > -1: // stroke band
				c, set = bgStroke, true
			default:
				c, set = bgFill, true
			}

			// Tunnel stroke (width 5 => radius 2.5, round caps hidden
			// under the node circles).
			if distPolyline(px, py, bez) <= 2.5 {
				c, set = tunnelBlue, true
			}

			// Nodes: filled circle r=8 with a 2px dark ring at r=8.
			for _, n := range []struct {
				cx, cy float64
				fill   color.NRGBA
			}{{20, 44, nodeGreen}, {44, 20, tunnelBlue}} {
				dc := math.Hypot(px-n.cx, py-n.cy)
				if dc <= 8 {
					c, set = n.fill, true
				}
				if math.Abs(dc-8) <= 1 {
					c, set = ringDark, true
				}
			}

			if set {
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return img
}

type pt struct{ x, y float64 }

// flattenBezier samples a cubic bezier into n line segments.
func flattenBezier(p0, p1, p2, p3 pt, n int) []pt {
	out := make([]pt, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		out[i] = pt{
			u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
			u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
		}
	}
	return out
}

// distPolyline returns the distance from (x,y) to the closest segment.
func distPolyline(x, y float64, poly []pt) float64 {
	best := math.MaxFloat64
	for i := 0; i+1 < len(poly); i++ {
		if d := distSegment(x, y, poly[i], poly[i+1]); d < best {
			best = d
		}
	}
	return best
}

func distSegment(x, y float64, a, b pt) float64 {
	vx, vy := b.x-a.x, b.y-a.y
	wx, wy := x-a.x, y-a.y
	t := 0.0
	if l2 := vx*vx + vy*vy; l2 > 0 {
		t = math.Max(0, math.Min(1, (wx*vx+wy*vy)/l2))
	}
	return math.Hypot(x-(a.x+t*vx), y-(a.y+t*vy))
}

// sdRoundRect is the signed distance to a rounded rectangle centered at
// (cx,cy) with half extents (hx,hy) and corner radius r.
func sdRoundRect(x, y, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(x-cx) - (hx - r)
	qy := math.Abs(y-cy) - (hy - r)
	ax, ay := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ax, ay) + math.Min(math.Max(qx, qy), 0) - r
}

// downscale box-averages src (master x master) to an n x n image.
// Averaging happens premultiplied so transparent corners blend cleanly.
func downscale(src *image.NRGBA, n int) *image.NRGBA {
	f := master / n
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a uint64
			for dy := 0; dy < f; dy++ {
				for dx := 0; dx < f; dx++ {
					c := src.NRGBAAt(x*f+dx, y*f+dy)
					al := uint64(c.A)
					r += uint64(c.R) * al
					g += uint64(c.G) * al
					b += uint64(c.B) * al
					a += al
				}
			}
			if a == 0 {
				continue
			}
			cnt := uint64(f * f)
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a), G: uint8(g / a), B: uint8(b / a),
				A: uint8(a / cnt),
			})
		}
	}
	return dst
}

// encodeICO packs PNG-compressed entries (valid since Windows Vista) for
// every size into a single .ico container.
func encodeICO(masterImg *image.NRGBA) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, s := range icoSizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, downscale(masterImg, s)); err != nil {
			return nil, err
		}
		entries = append(entries, entry{s, buf.Bytes()})
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), count.
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(entries))})
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w := byte(e.size) // 0 means 256
		if e.size >= 256 {
			w = 0
		}
		out.Write([]byte{w, w, 0, 0})
		binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&out, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	return out.Bytes(), nil
}

// encodeICNS packs PNG payloads into Apple's ICNS container: an 8-byte
// header ("icns" + total length) followed by chunks of 4-byte type +
// 4-byte big-endian length (header included) + data.
func encodeICNS(masterImg *image.NRGBA) ([]byte, error) {
	var body bytes.Buffer
	for _, e := range icnsEntries {
		var buf bytes.Buffer
		if err := png.Encode(&buf, downscale(masterImg, e.size)); err != nil {
			return nil, err
		}
		body.WriteString(e.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+buf.Len()))
		body.Write(buf.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), nil
}
