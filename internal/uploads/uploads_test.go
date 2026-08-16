package uploads

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Real magic-byte signatures -- http.DetectContentType only needs these
// leading bytes to sniff a real content type, not a fully valid image
// file, so these are the smallest inputs that exercise the real sniffing
// path SaveLogo depends on instead of trusting a claimed Content-Type.
var (
	pngSignature  = []byte("\x89PNG\r\n\x1a\n" + "rest of a fake png")
	jpegSignature = []byte("\xFF\xD8\xFF" + "rest of a fake jpeg")
	// http.DetectContentType needs more than the bare RIFF/WEBP FourCCs to
	// sniff image/webp -- verified directly against net/http's real
	// sniffer, not assumed: it also needs a recognized VP8 sub-chunk
	// marker right after ("VP8 ", "VP8L", or "VP8X"), matching a real
	// WebP container's actual structure.
	webpSignature = []byte("RIFF\x1a\x00\x00\x00WEBPVP8 " + "rest of a fake webp")
	gifSignature  = []byte("GIF89a" + "rest of a fake gif") // a real image type, but not one SaveLogo allows
)

// newMultipartFile builds a real multipart.File/*multipart.FileHeader
// pair by round-tripping through an actual multipart-encoded HTTP
// request -- the same code path r.FormFile("logo") uses in production,
// rather than hand-constructing fake values that might not exercise the
// same internals.
func newMultipartFile(t *testing.T, filename string, content []byte) (multipart.File, *multipart.FileHeader) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("logo", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	file, header, err := req.FormFile("logo")
	if err != nil {
		t.Fatalf("FormFile: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, header
}

// withTempBaseDir points BaseDir at a fresh t.TempDir() for the duration
// of one test, restoring the original value after -- so tests never write
// into the real repo tree (see BaseDir's own doc comment).
func withTempBaseDir(t *testing.T) {
	t.Helper()
	original := BaseDir
	BaseDir = t.TempDir()
	t.Cleanup(func() { BaseDir = original })
}

func TestSaveLogoAcceptsPNG(t *testing.T) {
	withTempBaseDir(t)
	file, header := newMultipartFile(t, "logo.png", pngSignature)

	path, err := SaveLogo("sunrise", file, header)
	if err != nil {
		t.Fatalf("SaveLogo: %v", err)
	}
	if path != "/static/uploads/sunrise/logo.png" {
		t.Errorf("unexpected public path: %s", path)
	}
	if _, err := os.Stat(filepath.Join(BaseDir, "sunrise", "logo.png")); err != nil {
		t.Errorf("expected file on disk: %v", err)
	}
}

func TestSaveLogoAcceptsJPEGAndWebP(t *testing.T) {
	withTempBaseDir(t)

	file, header := newMultipartFile(t, "logo.jpg", jpegSignature)
	path, err := SaveLogo("sunrise", file, header)
	if err != nil {
		t.Fatalf("SaveLogo (jpeg): %v", err)
	}
	if path != "/static/uploads/sunrise/logo.jpg" {
		t.Errorf("unexpected public path: %s", path)
	}

	file, header = newMultipartFile(t, "logo.webp", webpSignature)
	path, err = SaveLogo("sunrise", file, header)
	if err != nil {
		t.Fatalf("SaveLogo (webp): %v", err)
	}
	if path != "/static/uploads/sunrise/logo.webp" {
		t.Errorf("unexpected public path: %s", path)
	}
}

// TestSaveLogoReplacingFormatRemovesOldFile confirms a re-upload in a
// different format doesn't leave the previous logo.<ext> behind as an
// orphan -- see SaveLogo's own comment on why.
func TestSaveLogoReplacingFormatRemovesOldFile(t *testing.T) {
	withTempBaseDir(t)

	file, header := newMultipartFile(t, "logo.png", pngSignature)
	if _, err := SaveLogo("sunrise", file, header); err != nil {
		t.Fatalf("SaveLogo (png): %v", err)
	}
	file, header = newMultipartFile(t, "logo.jpg", jpegSignature)
	if _, err := SaveLogo("sunrise", file, header); err != nil {
		t.Fatalf("SaveLogo (jpg): %v", err)
	}

	if _, err := os.Stat(filepath.Join(BaseDir, "sunrise", "logo.png")); !os.IsNotExist(err) {
		t.Errorf("expected the old logo.png to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(BaseDir, "sunrise", "logo.jpg")); err != nil {
		t.Errorf("expected the new logo.jpg on disk: %v", err)
	}
}

func TestSaveLogoRejectsUnsupportedType(t *testing.T) {
	withTempBaseDir(t)

	cases := []struct {
		name    string
		content []byte
	}{
		{"gif (a real image type, just not an allowed one)", gifSignature},
		{"plain text pretending to be an image", []byte("not an image at all, just text")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, header := newMultipartFile(t, "logo.png", c.content) // a lying filename/extension shouldn't matter
			if _, err := SaveLogo("sunrise", file, header); err != ErrUnsupportedType {
				t.Errorf("expected ErrUnsupportedType, got %v", err)
			}
		})
	}
}

func TestSaveLogoRejectsInvalidSlug(t *testing.T) {
	withTempBaseDir(t)

	for _, slug := range []string{"", "../../etc/passwd", "Sunrise Gas", "sunrise/../../etc", "-sunrise", "sunrise-"} {
		t.Run(slug, func(t *testing.T) {
			file, header := newMultipartFile(t, "logo.png", pngSignature)
			if _, err := SaveLogo(slug, file, header); err != ErrInvalidSlug {
				t.Errorf("SaveLogo(%q): expected ErrInvalidSlug, got %v", slug, err)
			}
		})
	}
}

// TestSaveLogoRejectsOversizedHeaderSize confirms the fast-path rejection
// based on the client-declared size, before any bytes are even read.
func TestSaveLogoRejectsOversizedHeaderSize(t *testing.T) {
	withTempBaseDir(t)
	file, header := newMultipartFile(t, "logo.png", pngSignature)
	header.Size = MaxLogoBytes + 1

	if _, err := SaveLogo("sunrise", file, header); err != ErrFileTooLarge {
		t.Errorf("expected ErrFileTooLarge, got %v", err)
	}
}

// TestSaveLogoRejectsOversizedActualContent confirms the real enforcement
// (actual bytes read, not the client-declared size) -- a header.Size that
// understates the real content is still caught.
func TestSaveLogoRejectsOversizedActualContent(t *testing.T) {
	withTempBaseDir(t)

	oversized := make([]byte, MaxLogoBytes+1024)
	copy(oversized, pngSignature)
	file, header := newMultipartFile(t, "logo.png", oversized)
	// A multipart round trip already sets header.Size correctly from the
	// real body, so this test's "lie" is implicit: SaveLogo must reject
	// this purely from reading MaxLogoBytes+1 bytes, not by trusting
	// header.Size, which in this case happens to already be accurate --
	// TestSaveLogoRejectsOversizedHeaderSize above is what isolates the
	// header-only fast path instead.
	if header.Size <= MaxLogoBytes {
		t.Fatalf("test setup problem: header.Size (%d) should exceed MaxLogoBytes (%d)", header.Size, MaxLogoBytes)
	}

	if _, err := SaveLogo("sunrise", file, header); err != ErrFileTooLarge {
		t.Errorf("expected ErrFileTooLarge, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(BaseDir, "sunrise", "logo.png")); !os.IsNotExist(err) {
		t.Error("expected no partial file left behind on disk after a rejected oversized upload")
	}
}

// TestSaveLogoNeverTrustsClientContentType confirms the client-supplied
// Content-Type header on the multipart part is never trusted on its own
// -- a part that claims to be a PNG but whose actual bytes are something
// else entirely is rejected based on the real bytes.
func TestSaveLogoNeverTrustsClientContentType(t *testing.T) {
	withTempBaseDir(t)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{`form-data; name="logo"; filename="logo.png"`}
	header["Content-Type"] = []string{"image/png"} // lying -- the actual bytes below are plain text
	part, err := w.CreatePart(header)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write([]byte("definitely not a png")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	file, fileHeader, err := req.FormFile("logo")
	if err != nil {
		t.Fatalf("FormFile: %v", err)
	}
	defer file.Close()

	if _, err := SaveLogo("sunrise", file, fileHeader); err != ErrUnsupportedType {
		t.Errorf("expected ErrUnsupportedType for content that lies about being a PNG, got %v", err)
	}
}

// TestSaveLogoOverwritesSameFormat confirms a second upload in the SAME
// format actually replaces the first, rather than erroring or appending.
func TestSaveLogoOverwritesSameFormat(t *testing.T) {
	withTempBaseDir(t)

	file, header := newMultipartFile(t, "logo.png", pngSignature)
	if _, err := SaveLogo("sunrise", file, header); err != nil {
		t.Fatalf("SaveLogo (first): %v", err)
	}

	second := append(append([]byte{}, pngSignature...), []byte(" -- a different, longer, second upload")...)
	file, header = newMultipartFile(t, "logo.png", second)
	if _, err := SaveLogo("sunrise", file, header); err != nil {
		t.Fatalf("SaveLogo (second): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(BaseDir, "sunrise", "logo.png"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Error("expected the second upload's content to have replaced the first")
	}
}

// TestSaveLogoKeepsDifferentPlantsSeparate confirms two plants' logos
// never collide on disk.
func TestSaveLogoKeepsDifferentPlantsSeparate(t *testing.T) {
	withTempBaseDir(t)

	fileA, headerA := newMultipartFile(t, "logo.png", pngSignature)
	if _, err := SaveLogo("sunrise", fileA, headerA); err != nil {
		t.Fatalf("SaveLogo (sunrise): %v", err)
	}
	fileB, headerB := newMultipartFile(t, "logo.jpg", jpegSignature)
	if _, err := SaveLogo("coastal", fileB, headerB); err != nil {
		t.Fatalf("SaveLogo (coastal): %v", err)
	}

	if _, err := os.Stat(filepath.Join(BaseDir, "sunrise", "logo.png")); err != nil {
		t.Errorf("expected sunrise's logo on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(BaseDir, "coastal", "logo.jpg")); err != nil {
		t.Errorf("expected coastal's logo on disk: %v", err)
	}
}
