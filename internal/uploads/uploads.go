// Package uploads handles saving plant-supplied files -- currently just
// logos -- to local disk, served back out through the existing
// http.FileServer already mounted at /static/* (cmd/server/main.go's
// MountHandlers). Local disk was a deliberate, explicit choice for now,
// not S3/R2 -- matching this app's current single-instance deployment
// shape (one app container, bind-mounted volumes, no object storage
// anywhere else in the stack). Revisit if/when this app ever runs
// multiple replicas or moves to an ephemeral-filesystem host: a file
// saved here only exists on the instance that received the upload, so it
// won't be visible from a different replica, and won't survive a host
// migration that doesn't carry the volume with it.
package uploads

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrFileTooLarge    = errors.New("uploads: file exceeds the maximum allowed size")
	ErrUnsupportedType = errors.New("uploads: file must be a PNG, JPEG, or WebP image")
	ErrInvalidSlug     = errors.New("uploads: invalid plant slug")
)

// MaxLogoBytes caps a plant logo at 2 MiB -- generous for a small brand
// mark shown at a few dozen pixels on the customer page and receipt,
// without leaving the disk open to an effectively unbounded upload.
const MaxLogoBytes = 2 << 20

// slugPattern mirrors internal/plants.slugPattern exactly -- duplicated
// rather than imported, the same small-duplication precedent
// internal/plants.go's own validHexColor doc comment already explains for
// this codebase's small cross-package helpers. Specifically lets this
// package validate the shape of a slug on its own, without depending on
// internal/plants just for that, and guarantees a bad slug is rejected
// here even if a caller somehow skipped internal/plants' own validation
// -- this is what stands between an untrusted string and a filesystem
// path.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// allowedLogoTypes maps a sniffed content type -- via http.DetectContentType
// on the file's actual leading bytes, never the client-supplied
// Content-Type header, which is trivially spoofable -- to the file
// extension saved to disk. SVG is deliberately excluded: it can embed
// <script>, a real XSS vector if ever served with (or sniffed as) an
// HTML-adjacent content type, and every surface that could show a plant's
// logo is public.
var allowedLogoTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
}

// BaseDir is where logos are actually written -- inside the same
// web/static tree http.FileServer(http.Dir("web/static")) already serves
// at /static/*, so saving here needs no new route or handler to serve it
// back out. A relative path, so it (like http.Dir("web/static") itself,
// cmd/server/main.go) assumes the process's working directory is the repo
// root -- true for how this app is actually run (air/go run from the repo
// root), but not for `go test`, whose working directory is the package
// directory instead. A var, not a const, specifically so uploads_test.go
// can point it at a t.TempDir() instead of writing into the real repo
// tree during tests.
var BaseDir = "web/static/uploads"

// PublicPrefix is the URL path files under BaseDir are served at --
// independent of BaseDir's own value (see BaseDir's doc comment) so a
// test pointing BaseDir at a temp directory doesn't also need a matching
// real URL, since a test never actually serves the file over HTTP anyway.
const PublicPrefix = "/static/uploads"

// SaveLogo validates and saves a plant's uploaded logo, keyed by the
// plant's own slug (already unique, and filesystem-safe once validated
// against slugPattern above). Returns the public URL path to store in
// plants.logo_path, e.g. "/static/uploads/sunrise/logo.png".
//
// Called from the handler layer (cmd/server's provisionPlant) before
// plants.Create has run -- there's no plant id yet at that point, only a
// slug, so the slug is what a logo lives under. plants.ValidateParams
// runs before this (same ordering CreateSubaccount already relies on for
// the exact same reason): a submission missing some other required field
// should never reach here and write a file for nothing.
//
// file is read to completion or up to MaxLogoBytes+1, whichever comes
// first -- header.Size (the client-declared Content-Length for this
// part) is checked as a fast rejection, but never trusted on its own; the
// actual bytes read are what's enforced.
func SaveLogo(slug string, file multipart.File, header *multipart.FileHeader) (publicPath string, err error) {
	if !slugPattern.MatchString(slug) {
		return "", ErrInvalidSlug
	}
	if header != nil && header.Size > MaxLogoBytes {
		return "", ErrFileTooLarge
	}

	sniff := make([]byte, 512)
	n, err := io.ReadFull(file, sniff)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	sniff = sniff[:n]
	contentType := http.DetectContentType(sniff)
	ext, ok := allowedLogoTypes[contentType]
	if !ok {
		return "", ErrUnsupportedType
	}

	dir := filepath.Join(BaseDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// A fixed filename ("logo<ext>"), never anything derived from
	// header.Filename -- the client-supplied original filename is
	// untrusted input and is never used to build a filesystem path here,
	// so there's nothing for a crafted "../../etc/passwd" style filename
	// to reach. Overwriting any previous logo.<ext> here is deliberate:
	// re-uploading replaces it, there's no versioning need for a brand
	// mark. A re-upload in a *different* format (logo.png replaced by a
	// logo.jpg) would otherwise leave the old file behind as a harmless
	// but pointless orphan -- clear every other supported extension for
	// this slug first so exactly one logo file exists at a time.
	for _, otherExt := range allowedLogoTypes {
		if otherExt != ext {
			os.Remove(filepath.Join(dir, "logo"+otherExt))
		}
	}
	dest := filepath.Join(dir, "logo"+ext)

	out, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := out.Write(sniff); err != nil {
		os.Remove(dest)
		return "", err
	}
	// Copy at most one byte past the remaining budget: if that succeeds,
	// the real file is larger than MaxLogoBytes regardless of what
	// header.Size claimed, and is rejected rather than silently truncated.
	remaining := MaxLogoBytes - int64(len(sniff))
	written, err := io.Copy(out, io.LimitReader(file, remaining+1))
	if err != nil {
		os.Remove(dest)
		return "", err
	}
	if written > remaining {
		os.Remove(dest)
		return "", ErrFileTooLarge
	}

	// Built independently of BaseDir/dest, not by stripping a prefix off
	// the disk path -- that would break the moment BaseDir's value
	// doesn't literally start with "web/" (as in uploads_test.go, which
	// points BaseDir at a t.TempDir() instead of writing into the real
	// repo tree). PublicPrefix is the one place the two are tied
	// together, matching http.FileServer(http.Dir("web/static"))'s own
	// /static/* mount point (cmd/server/main.go).
	return PublicPrefix + "/" + slug + "/logo" + ext, nil
}
