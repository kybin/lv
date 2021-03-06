package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/inconsolata"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
	"golang.org/x/mobile/event/key"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/mouse"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
)

type playMode int

const (
	playRealTime = playMode(iota)
	playEveryFrame
)

func (p playMode) String() string {
	switch p {
	case playRealTime:
		return "playRealTime"
	case playEveryFrame:
		return "playEveryFrame"
	default:
		return "unknown"
	}
}

type event int

const (
	unknownEvent = event(iota)
	playPauseEvent
	seekNextEvent
	seekPrevEvent
	seekNextFrameEvent
	seekPrevFrameEvent
	playRealTimeEvent
	playEveryFrameEvent
)

// frameEvent created from playFramer and sended to window.
// So let window to know current frame is changed and what frame it is.
type frameEvent int

func main() {
	var fps float64
	flag.Float64Var(&fps, "fps", 24, "play frame per second")
	flag.Parse()

	// Find movie/sequence from input.
	seq := flag.Args()
	if len(seq) == 0 {
		// TODO: Do not exit even if there is no input.
		// We should have Open dialog.
		fmt.Fprintln(os.Stderr, "Usage: lv [-flag] <mov/seq>")
		flag.PrintDefaults()
		os.Exit(1)
	}
	// TODO: How could we notice whether input is movie or sequence?
	// For now, we only support sequence.

	driver.Main(func(s screen.Screen) {
		// Get initial size.
		firstImage, err := loadImage(seq[0])
		if err != nil {
			log.Fatal(err)
		}
		initSize := firstImage.Bounds().Max
		var initWidth = float32(initSize.X)
		var initHeight = float32(initSize.Y)

		// Make a window.
		width := initSize.X
		height := initSize.Y
		w, err := s.NewWindow(&screen.NewWindowOptions{Width: width, Height: height})
		if err != nil {
			log.Fatal(err)
		}
		defer w.Release()
		var winScale float32 = 1

		mode := playRealTime
		playEventChan := make(chan event)
		go playFramer(mode, fps, len(seq), w, playEventChan)

		// box is an area holds image. It preserves image ratio.
		var boxScale float32 = 1
		var boxOffX float32 // when 0.5, only left-half of image will showing.
		var boxOffY float32 // when 0.5, only top-half of image will showing.

		// If user pressing right mouse button,
		// move mouse right will zoom the image, and left will un-zoom.
		zooming := false
		var zoomCenterX float32

		// lastBoxScale keep box scale when zooming.
		var lastBoxScale float32 = 1

		// If user pressing middle mouse button,
		// move mouse will pan the image.
		panning := false
		var panCenterX float32
		var panCenterY float32

		// lastBoxOff[XY] keep box offsets when panning.
		var lastBoxOffX float32
		var lastBoxOffY float32

		imageRect := image.Rect(0, 0, width, height)

		// Keep textures so we can reuse it. (ex: play loop)
		texs := make([]screen.Texture, len(seq))

		drawFrame := 0

		for {
			switch e := w.NextEvent().(type) {
			case lifecycle.Event:
				if e.To == lifecycle.StageDead {
					return
				}

			case key.Event:
				if e.Code == key.CodeEscape || e.Rune == 'q' {
					return
				}
				if e.Code == key.CodeSpacebar && e.Direction == key.DirPress {
					playEventChan <- playPauseEvent
				}
				if e.Code == key.CodeLeftArrow && e.Direction == key.DirPress {
					playEventChan <- seekPrevEvent
				}
				if e.Code == key.CodeRightArrow && e.Direction == key.DirPress {
					playEventChan <- seekNextEvent
				}
				if e.Rune == ',' && e.Direction == key.DirPress {
					playEventChan <- seekPrevFrameEvent
				}
				if e.Rune == '.' && e.Direction == key.DirPress {
					playEventChan <- seekNextFrameEvent
				}
				if e.Rune == 'm' && e.Direction == key.DirPress {
					if mode == playRealTime {
						mode = playEveryFrame
						playEventChan <- playEveryFrameEvent
					} else {
						mode = playRealTime
						playEventChan <- playRealTimeEvent
					}
				}
				if e.Rune == 'f' && e.Direction == key.DirPress {
					boxScale = 1
					boxOffX = 0
					boxOffY = 0
				}

			case mouse.Event:
				switch e.Button {
				case mouse.ButtonRight: // zoom
					if e.Direction == mouse.DirPress {
						zooming = true
						panning = false
						zoomCenterX = e.X
						lastBoxScale = boxScale
						lastBoxOffX = boxOffX
						lastBoxOffY = boxOffY
					} else {
						zooming = false
					}
				case mouse.ButtonMiddle: // pan
					if e.Direction == mouse.DirPress {
						panning = true
						zooming = false
						panCenterX = e.X
						panCenterY = e.Y
						lastBoxOffX = boxOffX
						lastBoxOffY = boxOffY
					} else {
						panning = false
					}
				case mouse.ButtonNone: // Mouse moving
					if zooming {
						dx := e.X - zoomCenterX
						z := fit(dx, -100, 300, 0, 4)
						if lastBoxScale*z < 0.1 {
							// Make boxScale always bigger or equal than 0.1
							z = 0.1 / lastBoxScale
						}
						boxOffX = lastBoxOffX * z
						boxOffY = lastBoxOffY * z
						boxScale = lastBoxScale * z
					} else if panning {
						dx := e.X - panCenterX
						dy := e.Y - panCenterY
						boxOffX = lastBoxOffX + dx/initWidth/winScale
						boxOffY = lastBoxOffY + dy/initHeight/winScale
					}
				}

			case size.Event:
				width, height = e.WidthPx, e.HeightPx
				ws := float32(width) / initWidth
				hs := float32(height) / initHeight
				winScale = ws
				if hs < ws {
					winScale = hs
				}

			case frameEvent:
				drawFrame = int(e)

			case paint.Event:
				// Will paint after select statement. See below.
			}

			// After every event, we should redraw the window.
			imageRect = image.Rect(
				int((0.5+boxOffX-boxScale*0.5)*initWidth*winScale),
				int((0.5+boxOffY-boxScale*0.5)*initHeight*winScale),
				int((0.5+boxOffX+boxScale*0.5)*initWidth*winScale),
				int((0.5+boxOffY+boxScale*0.5)*initHeight*winScale),
			)

			var tex screen.Texture
			if texs[drawFrame] == nil {
				img, err := loadImage(seq[drawFrame])
				if err != nil {
					log.Fatal(err)
				}
				tex = imageTexture(s, img)
				texs[drawFrame] = tex
			} else {
				// loop
				tex = texs[drawFrame]
			}

			subTex := subtitleTexture(s, fmt.Sprintf("play frame: %v\n\n%v", drawFrame, mode))
			playbarTex := playbarTexture(s, width, 10, drawFrame, len(seq))

			w.DrawUniform(f64.Aff3{1, 0, 0, 0, 1, 0}, color.Black, image.Rect(0, 0, width, height), screen.Src, nil)
			w.Scale(imageRect, tex, tex.Bounds(), screen.Src, nil)
			w.Copy(image.Point{0, 0}, subTex, subTex.Bounds(), screen.Over, nil)
			w.Copy(image.Point{0, height - 10}, playbarTex, playbarTex.Bounds(), screen.Src, nil)
			w.Publish()
		}
	})
}

func imageTexture(s screen.Screen, img image.Image) screen.Texture {
	tex, err := s.NewTexture(img.Bounds().Max)
	if err != nil {
		log.Fatal(err)
	}
	buf, err := s.NewBuffer(img.Bounds().Max)
	if err != nil {
		tex.Release()
		log.Fatal(err)
	}
	rgba := buf.RGBA()
	draw.Copy(rgba, image.Point{}, img, img.Bounds(), draw.Src, nil)
	tex.Upload(image.Point{}, buf, rgba.Bounds())
	buf.Release()

	return tex
}

func subtitleTexture(s screen.Screen, tx string) screen.Texture {
	lines := strings.Split(tx, "\n")
	width := 0
	for _, l := range lines {
		w := 8 * utf8.RuneCountInString(l)
		if w > width {
			width = w
		}
	}
	height := 16 * len(lines)

	tex, err := s.NewTexture(image.Point{width, height})
	if err != nil {
		log.Fatal(err)
	}
	buf, err := s.NewBuffer(image.Point{width, height})
	if err != nil {
		log.Fatal(err)
	}
	rgba := buf.RGBA()

	drawer := font.Drawer{
		Dst:  rgba,
		Src:  image.White,
		Face: inconsolata.Regular8x16,
		Dot: fixed.Point26_6{
			Y: inconsolata.Regular8x16.Metrics().Ascent,
		},
	}
	for _, l := range lines {
		drawer.DrawString(l)
		drawer.Dot.X = 0
		drawer.Dot.Y += fixed.I(16)
	}

	tex.Upload(image.Point{}, buf, rgba.Bounds())
	buf.Release()

	return tex
}

func playbarTexture(s screen.Screen, width, height, frame, lenSeq int) screen.Texture {
	tex, err := s.NewTexture(image.Point{width, height})
	if err != nil {
		log.Fatal(err)
	}
	buf, err := s.NewBuffer(image.Point{width, height})
	if err != nil {
		log.Fatal(err)
	}
	rgba := buf.RGBA()

	// Draw background
	gray := color.Gray{64}
	draw.Copy(rgba, image.Point{}, image.NewUniform(gray), image.Rect(0, 0, width, height), draw.Src, nil)

	// Draw cursor
	yellow := color.RGBA{R: 255, G: 255, B: 0, A: 255}
	cs := int(float64(width) * float64(frame) / float64(lenSeq))
	cw := int(float64(width) / float64(lenSeq))
	cw++ // Integer represention of width shrinks. Draw one pixel larger always.
	draw.Copy(rgba, image.Pt(cs, 0), image.NewUniform(yellow), image.Rect(0, 0, cw, height), draw.Src, nil)

	tex.Upload(image.Point{}, buf, rgba.Bounds())
	buf.Release()

	return tex
}

// playFramer return playFrame channel that sends which frame should played at the time.
func playFramer(mode playMode, fps float64, seqLen int, w screen.Window, eventCh <-chan event) {
	playing := true
	start := time.Now()
	var f int
	for {
		select {
		case ev := <-eventCh:
			if playing {
				f += int(time.Since(start).Seconds() * fps)
				if f >= seqLen {
					f %= seqLen
				}
			}
			start = time.Now()

			switch ev {
			case playPauseEvent:
				if playing {
					playing = false
				} else {
					playing = true
				}
			case seekPrevEvent:
				f -= int(fps) // TODO: rounding for non-integer fps
				if f < 0 {
					f = 0
				}
			case seekNextEvent:
				f += int(fps) // TODO: rounding for non-integer fps
				if f >= seqLen {
					f = seqLen - 1
				}
			case seekPrevFrameEvent:
				// when seeking frames, player should stop.
				playing = false
				f -= 1
				if f < 0 {
					f = 0
				}
			case seekNextFrameEvent:
				// when seeking frames, player should stop.
				playing = false
				f += 1
				if f >= seqLen {
					f = seqLen - 1
				}
			case playRealTimeEvent:
				mode = playRealTime
			case playEveryFrameEvent:
				mode = playEveryFrame
			}
		case <-time.After(time.Second / time.Duration(fps)):
			if !playing {
				continue
			}
		}
		var tf int
		if mode == playRealTime {
			tf = f + int(time.Since(start).Seconds()*fps)
			if tf >= seqLen {
				tf %= seqLen
			}
		} else {
			f++
			if f >= seqLen {
				f %= seqLen
			}
			tf = f
			start = time.Now()
		}
		w.Send(frameEvent(tf))
	}
}

func loadImage(pth string) (image.Image, error) {
	f, err := os.Open(pth)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}
