package main

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/bits"
	"net"
	"time"
)

const (
	rfbSecurityNone = 1
	rfbSecurityVNC  = 2

	maxScreenPixels = 16 << 20 // 64 MiB of pixels at 4 bytes each.
	maxCutTextBytes = 16 << 20

	// How long to wait for the rest of a screen after a partial update.
	partialUpdateWait = 2 * time.Second
)

type rfbConn interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

// vncResponse answers the VNC authentication challenge: DES with the first 8
// password bytes, each with its bits reversed as the protocol specifies.
func vncResponse(password string, challenge []byte) ([]byte, error) {
	if len(challenge) != 16 {
		return nil, errors.New("VNC challenge must be 16 bytes")
	}
	var key [8]byte
	copy(key[:], password)
	for i, b := range key {
		key[i] = bits.Reverse8(b)
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	return response, nil
}

func rfbReason(r io.Reader) string {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil || length == 0 || length > 1024 {
		return "no reason given"
	}
	reason := make([]byte, length)
	if _, err := io.ReadFull(r, reason); err != nil {
		return "no reason given"
	}
	return string(reason)
}

// rfbHandshake authenticates and returns the framebuffer size. It joins as a
// shared client, so anyone already on the console stays connected.
func rfbHandshake(conn io.ReadWriter, password string) (width, height int, err error) {
	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return 0, 0, fmt.Errorf("read RFB version: %w", err)
	}
	if !bytes.HasPrefix(version, []byte("RFB 003.")) {
		return 0, 0, errors.New("server does not speak RFB 3.x")
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return 0, 0, err
	}

	var count [1]byte
	if _, err := io.ReadFull(conn, count[:]); err != nil {
		return 0, 0, fmt.Errorf("read RFB security types: %w", err)
	}
	if count[0] == 0 {
		return 0, 0, fmt.Errorf("VNC server refused the connection: %s", rfbReason(conn))
	}
	types := make([]byte, count[0])
	if _, err := io.ReadFull(conn, types); err != nil {
		return 0, 0, err
	}
	security := byte(0)
	if bytes.IndexByte(types, rfbSecurityVNC) >= 0 {
		security = rfbSecurityVNC
	} else if bytes.IndexByte(types, rfbSecurityNone) >= 0 {
		security = rfbSecurityNone
	} else {
		return 0, 0, fmt.Errorf("no supported VNC security type in %v", types)
	}
	if _, err := conn.Write([]byte{security}); err != nil {
		return 0, 0, err
	}
	if security == rfbSecurityVNC {
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(conn, challenge); err != nil {
			return 0, 0, fmt.Errorf("read VNC challenge: %w", err)
		}
		response, err := vncResponse(password, challenge)
		if err != nil {
			return 0, 0, err
		}
		if _, err := conn.Write(response); err != nil {
			return 0, 0, err
		}
	}
	var result uint32
	if err := binary.Read(conn, binary.BigEndian, &result); err != nil {
		return 0, 0, fmt.Errorf("read VNC security result: %w", err)
	}
	if result != 0 {
		return 0, 0, fmt.Errorf("VNC authentication failed: %s", rfbReason(conn))
	}

	if _, err := conn.Write([]byte{1}); err != nil { // ClientInit: shared.
		return 0, 0, err
	}
	var serverInit struct {
		Width, Height uint16
		PixelFormat   [16]byte
		NameLength    uint32
	}
	if err := binary.Read(conn, binary.BigEndian, &serverInit); err != nil {
		return 0, 0, fmt.Errorf("read VNC server init: %w", err)
	}
	if _, err := io.CopyN(io.Discard, conn, int64(serverInit.NameLength)); err != nil {
		return 0, 0, err
	}
	width, height = int(serverInit.Width), int(serverInit.Height)
	if width == 0 || height == 0 || width*height > maxScreenPixels {
		return 0, 0, fmt.Errorf("unsupported screen size %dx%d", width, height)
	}
	return width, height, nil
}

// rfbRequestScreen asks for 32-bit raw pixels (bytes B, G, R, X) of the whole
// screen. It sends no key or pointer events: capture is strictly view-only.
func rfbRequestScreen(conn io.Writer, width, height int) error {
	message := []byte{
		0, 0, 0, 0, // SetPixelFormat
		32, 24, 0, 1, // 32 bpp, depth 24, little-endian, true colour
		0, 255, 0, 255, 0, 255, // red, green and blue maximums
		16, 8, 0, // red, green and blue shifts
		0, 0, 0,
		2, 0, 0, 1, 0, 0, 0, 0, // SetEncodings: raw only
		3, 0, 0, 0, 0, 0, // FramebufferUpdateRequest: not incremental, at 0,0
	}
	message = binary.BigEndian.AppendUint16(message, uint16(width))
	message = binary.BigEndian.AppendUint16(message, uint16(height))
	_, err := conn.Write(message)
	return err
}

func rfbReadRect(conn io.Reader, screen *image.RGBA) (area int, err error) {
	var rect struct {
		X, Y, Width, Height uint16
		Encoding            int32
	}
	if err := binary.Read(conn, binary.BigEndian, &rect); err != nil {
		return 0, err
	}
	if rect.Encoding != 0 {
		return 0, fmt.Errorf("unexpected VNC encoding %d", rect.Encoding)
	}
	x, y, width, height := int(rect.X), int(rect.Y), int(rect.Width), int(rect.Height)
	bounds := screen.Bounds()
	if x+width > bounds.Dx() || y+height > bounds.Dy() {
		return 0, errors.New("VNC rectangle is outside the screen")
	}
	row := make([]byte, width*4)
	for line := 0; line < height; line++ {
		if _, err := io.ReadFull(conn, row); err != nil {
			return 0, err
		}
		offset := screen.PixOffset(x, y+line)
		for i := 0; i < width; i++ {
			screen.Pix[offset] = row[i*4+2]
			screen.Pix[offset+1] = row[i*4+1]
			screen.Pix[offset+2] = row[i*4]
			screen.Pix[offset+3] = 255
			offset += 4
		}
	}
	return width * height, nil
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// readScreen returns the VM's framebuffer. partial is true when the server
// stopped sending before covering the whole screen.
func readScreen(ctx context.Context, conn rfbConn, password string) (screen *image.RGBA, partial bool, err error) {
	width, height, err := rfbHandshake(conn, password)
	if err != nil {
		return nil, false, err
	}
	if err := rfbRequestScreen(conn, width, height); err != nil {
		return nil, false, err
	}
	screen = image.NewRGBA(image.Rect(0, 0, width, height))
	covered := 0
	for covered < width*height {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var messageType [1]byte
		if _, err := io.ReadFull(conn, messageType[:]); err != nil {
			if covered > 0 && isTimeout(err) {
				return screen, true, nil
			}
			return nil, false, fmt.Errorf("read VNC message: %w", err)
		}
		switch messageType[0] {
		case 0: // FramebufferUpdate
			var update struct {
				Padding uint8
				Rects   uint16
			}
			if err := binary.Read(conn, binary.BigEndian, &update); err != nil {
				return nil, false, err
			}
			for i := 0; i < int(update.Rects); i++ {
				area, err := rfbReadRect(conn, screen)
				if err != nil {
					if covered > 0 && isTimeout(err) {
						return screen, true, nil
					}
					return nil, false, fmt.Errorf("read VNC rectangle: %w", err)
				}
				covered += area
			}
			if covered < width*height {
				if err := conn.SetReadDeadline(shorterDeadline(ctx, partialUpdateWait)); err != nil {
					return nil, false, err
				}
			}
		case 1: // SetColourMapEntries
			var header struct {
				Padding uint8
				First   uint16
				Count   uint16
			}
			if err := binary.Read(conn, binary.BigEndian, &header); err != nil {
				return nil, false, err
			}
			if _, err := io.CopyN(io.Discard, conn, int64(header.Count)*6); err != nil {
				return nil, false, err
			}
		case 2: // Bell
		case 3: // ServerCutText
			var header struct {
				Padding [3]byte
				Length  uint32
			}
			if err := binary.Read(conn, binary.BigEndian, &header); err != nil {
				return nil, false, err
			}
			if header.Length > maxCutTextBytes {
				return nil, false, errors.New("VNC clipboard message is too large")
			}
			if _, err := io.CopyN(io.Discard, conn, int64(header.Length)); err != nil {
				return nil, false, err
			}
		default:
			return nil, false, fmt.Errorf("unknown VNC message type %d", messageType[0])
		}
	}
	return screen, false, nil
}

func shorterDeadline(ctx context.Context, wait time.Duration) time.Time {
	deadline := time.Now().Add(wait)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		return limit
	}
	return deadline
}

// encodeScreen scales the screen down to maxWidth, averaging source pixels so
// that small text stays recognisable, and encodes it as a JPEG.
func encodeScreen(screen *image.RGBA, maxWidth int) (data []byte, width, height int, err error) {
	source := screen.Bounds()
	output := screen
	if source.Dx() > maxWidth {
		targetHeight := max(source.Dy()*maxWidth/source.Dx(), 1)
		output = image.NewRGBA(image.Rect(0, 0, maxWidth, targetHeight))
		for y := 0; y < targetHeight; y++ {
			y0 := y * source.Dy() / targetHeight
			y1 := max((y+1)*source.Dy()/targetHeight, y0+1)
			for x := 0; x < maxWidth; x++ {
				x0 := x * source.Dx() / maxWidth
				x1 := max((x+1)*source.Dx()/maxWidth, x0+1)
				var red, green, blue, samples int
				for sy := y0; sy < y1; sy++ {
					offset := screen.PixOffset(x0, sy)
					for sx := x0; sx < x1; sx++ {
						red += int(screen.Pix[offset])
						green += int(screen.Pix[offset+1])
						blue += int(screen.Pix[offset+2])
						samples++
						offset += 4
					}
				}
				target := output.PixOffset(x, y)
				output.Pix[target] = uint8(red / samples)
				output.Pix[target+1] = uint8(green / samples)
				output.Pix[target+2] = uint8(blue / samples)
				output.Pix[target+3] = 255
			}
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, output, &jpeg.Options{Quality: 70}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode screen: %w", err)
	}
	return buffer.Bytes(), output.Bounds().Dx(), output.Bounds().Dy(), nil
}
