package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"strings"
)

func validateHelmArchive(data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("chart archive is invalid")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	total := int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("chart archive is invalid")
		}
		clean := path.Clean(h.Name)
		if strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return errors.New("chart archive path escapes")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > helmBundleLimit {
			return errors.New("chart archive entry is unsupported")
		}
		total += h.Size
		if total > helmBundleLimit {
			return errors.New("chart archive exceeds limit")
		}
		raw, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil {
			return errors.New("chart archive is unreadable")
		}
		if strings.HasSuffix(clean, ".tgz") {
			return errors.New("nested chart archives require source review")
		}
		if err = validateHelmSourceFile(clean, raw); err != nil {
			return err
		}
	}
}
