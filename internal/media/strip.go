package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A phone photo carries where it was taken (GPS), the camera and the time in its
// metadata. Where attachments go out anonymized, a picture is sent as a copy without
// it: JPEG loses its EXIF, XMP, IPTC and comments but keeps its orientation, so it is
// not shown sideways; PNG loses its text and EXIF chunks; WebP its EXIF and XMP. The
// pixels are copied as they are, never re-encoded.

// Stripped is what Strip did with a file.
type Stripped int

const (
	// NotAPicture: nothing to clean; the file is sent as it is.
	NotAPicture Stripped = iota
	// Cleaned: the copy has no metadata but its orientation.
	Cleaned
	// Kept: a picture whose metadata kith cannot remove (HEIC); sent as it is.
	Kept
)

// errMalformed is a picture whose structure could not be followed: sending it as it is
// could leak what it was meant to drop, so it is refused instead.
var errMalformed = errors.New("media: the picture's structure could not be read, so its metadata could not be removed")

// Strip writes a copy of the picture at path without its metadata into dir (empty: the
// user's cache, which the daemon can read where its /tmp is private) and returns the
// copy's path. For anything else it returns path itself.
func Strip(path, dir string) (string, Stripped, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the file the person chose to send
	if err != nil {
		return "", NotAPicture, fmt.Errorf("media: read %s: %w", path, err)
	}
	var clean []byte
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8}):
		clean, err = stripJPEG(data)
	case bytes.HasPrefix(data, pngSignature):
		clean, err = stripPNG(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		clean, err = stripWebP(data)
	case isHEIF(data):
		return path, Kept, nil
	default:
		return path, NotAPicture, nil
	}
	if err != nil {
		return "", NotAPicture, err
	}
	if dir == "" {
		base, cerr := os.UserCacheDir()
		if cerr != nil {
			return "", NotAPicture, fmt.Errorf("media: no cache directory: %w", cerr)
		}
		dir = filepath.Join(base, "kith", "outgoing")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", NotAPicture, fmt.Errorf("media: create %s: %w", dir, err)
	}
	tmp, err := os.MkdirTemp(dir, "send-")
	if err != nil {
		return "", NotAPicture, fmt.Errorf("media: a place for the clean copy: %w", err)
	}
	out := filepath.Join(tmp, filepath.Base(path))
	if err = os.WriteFile(out, clean, 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return "", NotAPicture, fmt.Errorf("media: write the clean copy: %w", err)
	}
	return out, Cleaned, nil
}

// Unstrip removes a copy Strip made; path itself is left alone.
func Unstrip(copyPath, path string) {
	if copyPath != "" && copyPath != path {
		_ = os.RemoveAll(filepath.Dir(copyPath))
	}
}

// isHEIF reports an ISO-BMFF file branded as HEIF/HEIC/AVIF stills.
func isHEIF(data []byte) bool {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	switch string(data[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif":
		return true
	}
	return false
}

// JPEG markers Strip treats specially.
const (
	markerSOS   = 0xDA
	markerAPP0  = 0xE0
	markerAPP1  = 0xE1 // EXIF, XMP
	markerAPP2  = 0xE2 // ICC profile: kept, it is how the colors look
	markerAPP3  = 0xE3
	markerAPP14 = 0xEE // Adobe's color transform: kept
	markerAPPF  = 0xEF
	markerCOM   = 0xFE
)

// stripJPEG drops every APPn segment but JFIF (APP0), the ICC profile (APP2) and Adobe's
// color transform (APP14), and every comment, then writes the orientation back as the
// one EXIF tag. Everything from the start of scan on is copied as it is.
func stripJPEG(data []byte) ([]byte, error) {
	var kept [][]byte
	orientation := uint16(0)
	pos := 2
	for {
		if pos+2 > len(data) || data[pos] != 0xFF {
			return nil, errMalformed
		}
		marker := data[pos+1]
		if marker == 0xFF { // fill byte
			pos++
			continue
		}
		if marker == markerSOS {
			break
		}
		if pos+4 > len(data) {
			return nil, errMalformed
		}
		end := pos + 2 + int(binary.BigEndian.Uint16(data[pos+2:]))
		if end < pos+4 || end > len(data) {
			return nil, errMalformed
		}
		switch {
		case marker == markerAPP1:
			if o, ok := exifOrientation(data[pos+4 : end]); ok {
				orientation = o
			}
		case marker == markerCOM, marker >= markerAPP3 && marker <= markerAPPF && marker != markerAPP14:
		default:
			kept = append(kept, data[pos:end])
		}
		pos = end
	}
	out := bytes.NewBuffer(make([]byte, 0, len(data)))
	out.Write(data[:2])
	// JFIF, when there is one, must stay first; the orientation goes right after it.
	if len(kept) > 0 && kept[0][1] == markerAPP0 {
		out.Write(kept[0])
		kept = kept[1:]
	}
	if orientation > 1 {
		out.Write(orientationSegment(orientation))
	}
	for _, segment := range kept {
		out.Write(segment)
	}
	out.Write(data[pos:])
	return out.Bytes(), nil
}

// exifOrientation reads tag 0x0112 from IFD0 of an APP1 EXIF body.
func exifOrientation(body []byte) (uint16, bool) {
	tiff, ok := bytes.CutPrefix(body, []byte("Exif\x00\x00"))
	if !ok || len(tiff) < 8 {
		return 0, false
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, false
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd+2 > len(tiff) || ifd < 8 {
		return 0, false
	}
	count := int(order.Uint16(tiff[ifd:]))
	for i := range count {
		entry := ifd + 2 + 12*i
		if entry+12 > len(tiff) {
			return 0, false
		}
		if order.Uint16(tiff[entry:]) == 0x0112 && order.Uint16(tiff[entry+2:]) == 3 {
			o := order.Uint16(tiff[entry+8:])
			return o, o >= 1 && o <= 8
		}
	}
	return 0, false
}

// orientationSegment is an APP1 EXIF segment holding nothing but the orientation.
func orientationSegment(orientation uint16) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("Exif\x00\x00MM\x00\x2A")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))               // IFD0 follows the header
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))               // one entry
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0x0112))          // Orientation
	_ = binary.Write(&tiff, binary.BigEndian, uint16(3))               // SHORT
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))               // one value
	_ = binary.Write(&tiff, binary.BigEndian, uint32(orientation)<<16) // left-justified
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))               // no next IFD
	segment := []byte{0xFF, markerAPP1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(2+tiff.Len())) // #nosec G115 -- 34 bytes
	return append(segment, tiff.Bytes()...)
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// pngDropped are the chunks that say who, where or when: text, EXIF and the time.
var pngDropped = map[string]bool{"tEXt": true, "zTXt": true, "iTXt": true, "eXIf": true, "tIME": true}

// stripPNG copies every chunk but the dropped ones, CRCs and all.
func stripPNG(data []byte) ([]byte, error) {
	out := bytes.NewBuffer(make([]byte, 0, len(data)))
	out.Write(pngSignature)
	pos := len(pngSignature)
	for pos < len(data) {
		if pos+8 > len(data) {
			return nil, errMalformed
		}
		size := int(binary.BigEndian.Uint32(data[pos:]))
		kind := string(data[pos+4 : pos+8])
		end := pos + 12 + size
		if size < 0 || end > len(data) {
			return nil, errMalformed
		}
		if !pngDropped[kind] {
			out.Write(data[pos:end])
		}
		pos = end
		if kind == "IEND" {
			break
		}
	}
	return out.Bytes(), nil
}

// stripWebP drops the EXIF and XMP chunks and clears their flags in VP8X.
func stripWebP(data []byte) ([]byte, error) {
	var body bytes.Buffer
	body.WriteString("WEBP")
	pos := 12
	for pos < len(data) {
		if pos+8 > len(data) {
			return nil, errMalformed
		}
		kind := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4:]))
		end := pos + 8 + size + size%2
		if end > len(data) {
			return nil, errMalformed
		}
		switch kind {
		case "EXIF", "XMP ":
		case "VP8X":
			chunk := bytes.Clone(data[pos:end])
			if size > 0 {
				chunk[8] &^= 0x08 | 0x04 // EXIF and XMP present
			}
			body.Write(chunk)
		default:
			body.Write(data[pos:end])
		}
		pos = end
	}
	out := make([]byte, 8, 8+body.Len())
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(body.Len())) // #nosec G115 -- no larger than the RIFF it came from
	return append(out, body.Bytes()...), nil
}
