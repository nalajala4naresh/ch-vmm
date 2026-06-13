package volumeutil

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/klauspost/compress/zstd"

	syncpool "github.com/mostynb/zstdpool-syncpool"
)

var onceEncPool sync.Once
var encoderPool *sync.Pool

var onceDecPool sync.Once
var decoderPool *sync.Pool

var errDecoderPoolFail = errors.New("failed to get decoder from pool")
var errEncoderPoolFail = errors.New("failed to get encoder from pool")

var zstdFastestLevel = zstd.WithEncoderLevel(zstd.SpeedFastest)

var encoder, _ = zstd.NewWriter(nil, zstdFastestLevel) // TODO: raise WithEncoderConcurrency ?
var decoder, _ = zstd.NewReader(nil)

func init() {

	onceDecPool.Do(func() {
		decoderPool = syncpool.NewDecoderPool(
			zstd.WithDecoderConcurrency(1))
	})

	onceEncPool.Do(func() {
		encoderPool = syncpool.NewEncoderPool(
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderLevel(zstd.SpeedFastest))
	})

}

type Zstd struct{}

type zstdEncoderWrapper struct {
	*syncpool.EncoderWrapper
}

func (w *zstdEncoderWrapper) Close() error {
	err := w.EncoderWrapper.Close()
	encoderPool.Put(w.EncoderWrapper)
	return err
}

func (Zstd) GetDecoder(in io.ReadCloser) (io.ReadCloser, error) {
	dec, ok := decoderPool.Get().(*syncpool.DecoderWrapper)
	if !ok {
		return nil, errDecoderPoolFail
	}
	err := dec.Reset(in)
	if err != nil {
		decoderPool.Put(dec)
		return nil, err
	}
	return dec.IOReadCloser(), nil
}

func (Zstd) GetEncoder(out io.WriteCloser) (*zstdEncoderWrapper, error) {
	enc, ok := encoderPool.Get().(*syncpool.EncoderWrapper)
	if !ok {
		return nil, errEncoderPoolFail
	}
	enc.Reset(out)
	return &zstdEncoderWrapper{enc}, nil
}

func (Zstd) DecodeAll(in []byte) ([]byte, error) {
	return decoder.DecodeAll(in, nil)
}

func (Zstd) EncodeAll(in []byte) []byte {
	return encoder.EncodeAll(in, nil)
}

func CreateArchive(destination string, files []string) error {
	outstream, err := os.Create(destination)
	if err != nil {
		log.Fatalln("Error writing archive:", err)
	}

	// zstd
	zstd := Zstd{}
	encoder, _ := zstd.GetEncoder(outstream)
	tw := tar.NewWriter(encoder)
	defer tw.Close()

	for _, file := range files {
		err := addToArchive(tw, file)
		if err != nil {
			return err
		}
	}

	return nil

}

func ExtractArchive(destDir string, file string) error {

	// zstd
	zstd := Zstd{}
	rc, err := os.Open(file)
	if err != nil {
		log.Fatalln(fmt.Sprintf("Error %s Opening  archive file %s :", err, file))

	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	zstd_decoder, _ := zstd.GetDecoder(rc)
	tarReader := tar.NewReader(zstd_decoder)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return fmt.Errorf("error reading tar header: %w", err)
		}

		// Full target path

		if header.Typeflag == tar.TypeReg {

			filename := filepath.Base(header.Name)

			target := filepath.Join(destDir, filename)

			outFile, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}

			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("copy failed: %w", err)
			}

			outFile.Close()

		}
	}

	//now create a Done file for
	doneFile := filepath.Join(destDir, "done.txt")
	donefd, err := os.Create(doneFile)
	if err != nil {
		return fmt.Errorf("failed to create Done file: %w", err)
	}
	donefd.Close()

	return nil
}

func addToArchive(tw *tar.Writer, filename string) error {
	// Open the file which will be written into the archive
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// Get FileInfo about our file providing file size, mode, etc.
	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Create a tar Header from the FileInfo data
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}

	header.Name = filename

	// Write file header to the tar archive
	err = tw.WriteHeader(header)
	if err != nil {
		return err
	}

	// Copy file content to tar archive
	_, err = io.Copy(tw, file)
	if err != nil {
		return err
	}

	return nil
}
