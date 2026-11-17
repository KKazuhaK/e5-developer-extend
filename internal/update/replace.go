package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxExtract bounds one extracted program.
const maxExtract = 300 << 20

// extractTarGz writes the regular file called name (any folder in the
// archive) to dst. Nothing else in the archive is touched, and no archive
// path is ever used to build a destination.
func extractTarGz(archive, name, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(archive), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s does not contain %s", filepath.Base(archive), name)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(archive), err)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != name {
			continue
		}
		return writeLimited(dst, tr)
	}
}

// extractZip writes each of names (a regular file of the archive, any folder) into dir.
func extractZip(archive string, names []string, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(archive), err)
	}
	defer zr.Close()
	for _, want := range names {
		var found *zip.File
		for _, zf := range zr.File {
			if !zf.FileInfo().IsDir() && zf.FileInfo().Mode().IsRegular() && path.Base(zf.Name) == want {
				found = zf
				break
			}
		}
		if found == nil {
			return fmt.Errorf("%s does not contain %s", filepath.Base(archive), want)
		}
		rc, err := found.Open()
		if err != nil {
			return err
		}
		err = writeLimited(filepath.Join(dir, want), rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeLimited(dst string, r io.Reader) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxExtract+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxExtract {
		err = errors.New("a file in the archive is larger than allowed")
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// asideSuffix marks a program that was moved out of the way while it ran.
const asideSuffix = ".old"

// replaceFile puts the program at src in place of dst.
//
// By default the new file is written next to dst and renamed over it, which
// is atomic and works while the old program runs (Linux, macOS). With aside
// (Windows) the running file cannot be replaced or deleted, but it can be
// renamed, so the old file is renamed to dst.old first and the new one is
// written under the old name; if that fails the old file is put back.
// CleanupAside removes the .old files later.
func replaceFile(dst, src string, aside bool) error {
	mode := fs.FileMode(0o755)
	if fi, err := os.Stat(dst); err == nil {
		mode = fi.Mode().Perm() | 0o111
	}
	if aside {
		old := dst + asideSuffix
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			return fmt.Errorf("move %s aside: %w", filepath.Base(dst), err)
		}
		if err := copyFile(src, dst, mode); err != nil {
			_ = os.Remove(dst)
			if rerr := os.Rename(old, dst); rerr != nil {
				return fmt.Errorf("write %s: %w (and it could not be put back: %v)", filepath.Base(dst), err, rerr)
			}
			return fmt.Errorf("write %s: %w", filepath.Base(dst), err)
		}
		return nil
	}
	tmp := dst + ".new"
	if err := copyFile(src, tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", filepath.Base(dst), err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// undoAside puts the program that replaceFile moved aside back in its place.
func undoAside(dst string) {
	if err := os.Rename(dst+asideSuffix, dst); err != nil {
		return
	}
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dst, mode)
	}
	return err
}

// CleanupAside removes the e5extend*.exe.old files an earlier upgrade left in
// dir. A file still in use stays; it is tried again next time.
func CleanupAside(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "e5extend") && strings.HasSuffix(n, ".exe"+asideSuffix) && !e.IsDir() {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// dirWritable reports whether a file can be created in dir.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".e5extend-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
