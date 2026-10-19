package media

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// secret is what a phone writes into a photo and must not leave with it.
const secret = "GPS 32.0853N 34.7818E Pixel 9"

func picture() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	return img
}

// exifSegment is an APP1 EXIF segment with an orientation and the secret after it.
func exifSegment(t *testing.T, order binary.ByteOrder, orientation uint16) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("Exif\x00\x00")
	if order == binary.LittleEndian {
		tiff.WriteString("II\x2A\x00")
	} else {
		tiff.WriteString("MM\x00\x2A")
	}
	_ = binary.Write(&tiff, order, uint32(8))
	_ = binary.Write(&tiff, order, uint16(2))
	// A tag before the orientation, then the orientation.
	_ = binary.Write(&tiff, order, []uint16{0x010F, 2})
	_ = binary.Write(&tiff, order, uint32(len(secret)))
	_ = binary.Write(&tiff, order, uint32(38))
	_ = binary.Write(&tiff, order, []uint16{0x0112, 3, 0, 1})
	_ = binary.Write(&tiff, order, []uint16{orientation, 0}) // a SHORT, left-justified
	_ = binary.Write(&tiff, order, uint32(0))
	tiff.WriteString(secret)
	return segment(0xE1, tiff.Bytes())
}

func segment(marker byte, body []byte) []byte {
	out := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(2+len(body)))
	return append(out, body...)
}

// phoneJPEG is a JPEG as a phone writes it: JFIF, EXIF with the orientation, XMP, ICC,
// IPTC and a comment, all before the picture.
func phoneJPEG(t *testing.T, order binary.ByteOrder, orientation uint16) []byte {
	t.Helper()
	var enc bytes.Buffer
	if err := jpeg.Encode(&enc, picture(), nil); err != nil {
		t.Fatal(err)
	}
	plain := enc.Bytes() // SOI, then the encoder's own segments
	var out bytes.Buffer
	out.Write(plain[:2])
	out.Write(segment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")))
	out.Write(exifSegment(t, order, orientation))
	out.Write(segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>"+secret+"</x:xmpmeta>")))
	out.Write(segment(0xE2, []byte("ICC_PROFILE\x00\x01\x01profile")))
	out.Write(segment(0xED, []byte("Photoshop 3.0\x00"+secret)))
	out.Write(segment(0xFE, []byte(secret)))
	out.Write(plain[2:])
	return out.Bytes()
}

func stripBytes(t *testing.T, name string, data []byte) (string, Stripped, []byte) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, what, err := Strip(path, filepath.Join(dir, "outgoing"))
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if what == Cleaned && out == path {
		t.Fatal("cleaned in place: the person's own file was changed")
	}
	return out, what, got
}

// A phone JPEG leaves without its location, camera, XMP, IPTC or comment, keeps its
// orientation (either byte order) and its color profile, and still decodes.
func TestAJPEGLeavesWithoutWhereItWasTaken(t *testing.T) {
	t.Parallel()
	for name, order := range map[string]binary.ByteOrder{"little-endian": binary.LittleEndian, "big-endian": binary.BigEndian} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, what, got := stripBytes(t, "IMG_0001.jpg", phoneJPEG(t, order, 6))
			if what != Cleaned {
				t.Fatalf("Strip = %v, want Cleaned", what)
			}
			if bytes.Contains(got, []byte(secret)) {
				t.Error("the metadata is still in the copy")
			}
			if !bytes.Contains(got, []byte("ICC_PROFILE")) {
				t.Error("the color profile was dropped; it is how the picture looks, not who took it")
			}
			if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
				t.Errorf("the copy does not decode: %v", err)
			}
			// JFIF first, then the orientation as the only EXIF.
			if got[2] != 0xFF || got[3] != 0xE0 || !bytes.HasPrefix(got[6:], []byte("JFIF")) {
				t.Errorf("JFIF is not first: % x", got[:12])
			}
			jfifEnd := 4 + int(binary.BigEndian.Uint16(got[4:]))
			if got[jfifEnd+1] != 0xE1 {
				t.Fatalf("no EXIF after JFIF: % x", got[jfifEnd:jfifEnd+4])
			}
			exifEnd := jfifEnd + 2 + int(binary.BigEndian.Uint16(got[jfifEnd+2:]))
			if o, ok := exifOrientation(got[jfifEnd+4 : exifEnd]); !ok || o != 6 {
				t.Errorf("orientation = %d, %v; want 6 kept, or the photo shows sideways", o, ok)
			}
			Unstrip(out, "")
			if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Errorf("the copy's folder stayed after Unstrip: %v", err)
			}
		})
	}
}

// An upright photo needs no orientation: no EXIF is written at all.
func TestAnUprightJPEGGetsNoEXIF(t *testing.T) {
	t.Parallel()
	_, _, got := stripBytes(t, "a.jpg", phoneJPEG(t, binary.BigEndian, 1))
	if bytes.Contains(got, []byte("Exif\x00\x00")) {
		t.Error("an upright photo was given an EXIF segment")
	}
}

func pngChunk(kind string, data []byte) []byte {
	out := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	copy(out[4:], kind)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[4:]))
}

// A PNG leaves without its text, EXIF or time chunks and still decodes.
func TestAPNGLeavesWithoutItsTextOrEXIF(t *testing.T) {
	t.Parallel()
	var enc bytes.Buffer
	if err := png.Encode(&enc, picture()); err != nil {
		t.Fatal(err)
	}
	plain := enc.Bytes()
	ihdrEnd := 8 + 12 + 13
	var with bytes.Buffer
	with.Write(plain[:ihdrEnd])
	with.Write(pngChunk("tEXt", []byte("Location\x00"+secret)))
	with.Write(pngChunk("iTXt", []byte("Comment\x00\x00\x00\x00\x00"+secret)))
	with.Write(pngChunk("eXIf", []byte("MM\x00\x2A"+secret)))
	with.Write(pngChunk("tIME", []byte{0x07, 0xEA, 10, 20, 8, 0, 0}))
	with.Write(plain[ihdrEnd:])

	_, what, got := stripBytes(t, "shot.png", with.Bytes())
	if what != Cleaned {
		t.Fatalf("Strip = %v, want Cleaned", what)
	}
	if bytes.Contains(got, []byte(secret)) || bytes.Contains(got, []byte("tIME")) {
		t.Error("the metadata is still in the copy")
	}
	if !bytes.Equal(got, plain) {
		t.Error("the copy differs from the picture without its metadata")
	}
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Errorf("the copy does not decode: %v", err)
	}
}

func riffChunk(kind string, data []byte) []byte {
	out := []byte(kind)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// A WebP leaves without its EXIF and XMP chunks, its header no longer claims them, and
// the container's size adds up.
func TestAWebPLeavesWithoutItsEXIFOrXMP(t *testing.T) {
	t.Parallel()
	vp8x := make([]byte, 10)
	vp8x[0] = 0x20 | 0x08 | 0x04 // ICC, EXIF, XMP
	var chunks bytes.Buffer
	chunks.WriteString("WEBP")
	chunks.Write(riffChunk("VP8X", vp8x))
	chunks.Write(riffChunk("ICCP", []byte("profile")))
	chunks.Write(riffChunk("VP8L", []byte("pixels!")))
	chunks.Write(riffChunk("EXIF", []byte(secret)))
	chunks.Write(riffChunk("XMP ", []byte(secret)))
	data := []byte("RIFF")
	data = binary.LittleEndian.AppendUint32(data, uint32(chunks.Len()))
	data = append(data, chunks.Bytes()...)

	_, what, got := stripBytes(t, "x.webp", data)
	if what != Cleaned {
		t.Fatalf("Strip = %v, want Cleaned", what)
	}
	if bytes.Contains(got, []byte(secret)) {
		t.Error("the metadata is still in the copy")
	}
	if size := binary.LittleEndian.Uint32(got[4:]); int(size) != len(got)-8 {
		t.Errorf("RIFF size = %d, want %d", size, len(got)-8)
	}
	if flags := got[20]; flags != 0x20 {
		t.Errorf("VP8X flags = %#x, want only ICC (0x20)", flags)
	}
	if !bytes.Contains(got, []byte("VP8Lpixels")) && !bytes.Contains(got, []byte("pixels!")) {
		t.Error("the picture itself was dropped")
	}
}

// HEIC is sent as it is and said so; anything that is not a picture is left alone;
// a JPEG whose structure cannot be followed is refused rather than sent with its metadata.
func TestWhatStripLeavesAlone(t *testing.T) {
	t.Parallel()
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for name, tc := range map[string]struct {
		data []byte
		want Stripped
	}{
		"HEIC":     {heic, Kept},
		"a PDF":    {[]byte("%PDF-1.7\n" + secret), NotAPicture},
		"an empty": {nil, NotAPicture},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, what, got := stripBytes(t, "f", tc.data)
			if what != tc.want {
				t.Errorf("Strip = %v, want %v", what, tc.want)
			}
			if filepath.Base(out) != "f" || filepath.Base(filepath.Dir(out)) == "outgoing" {
				t.Errorf("Strip copied a file it leaves alone: %s", out)
			}
			if !bytes.Equal(got, tc.data) {
				t.Error("the file changed")
			}
		})
	}

	broken := phoneJPEG(t, binary.BigEndian, 6)[:40]
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.jpg")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Strip(path, filepath.Join(dir, "outgoing")); err == nil {
		t.Error("a JPEG cut short was passed; sending it as it is would keep its metadata")
	}
}
