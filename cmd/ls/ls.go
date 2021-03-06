package ls

import (
	"archive/tar"
	"encoding/binary"
	"io"
	"os"

	"github.com/itchio/arkive/zip"
	"github.com/itchio/butler/comm"
	"github.com/itchio/butler/mansion"
	"github.com/itchio/httpkit/progress"
	"github.com/itchio/savior/seeksource"
	"github.com/itchio/wharf/eos"
	"github.com/itchio/wharf/eos/option"
	"github.com/itchio/wharf/pwr"
	"github.com/itchio/wharf/tlc"
	"github.com/itchio/wharf/wire"
	"github.com/pkg/errors"
)

var args = struct {
	file *string
}{}

func Register(ctx *mansion.Context) {
	cmd := ctx.App.Command("ls", "Prints the list of files, dirs and symlinks contained in a patch file, signature file, or archive")
	args.file = cmd.Arg("file", "A file you'd like to list the contents of").Required().String()
	ctx.Register(cmd, do)
}

func do(ctx *mansion.Context) {
	ctx.Must(Do(ctx, *args.file))
}

func Do(ctx *mansion.Context, inPath string) error {
	consumer := comm.NewStateConsumer()

	reader, err := eos.Open(inPath, option.WithConsumer(consumer))
	if err != nil {
		return errors.WithStack(err)
	}

	path := eos.Redact(inPath)

	defer reader.Close()

	stats, err := reader.Stat()
	if os.IsNotExist(err) {
		comm.Dief("%s: no such file or directory", path)
	}
	if err != nil {
		return errors.WithStack(err)
	}

	if stats.IsDir() {
		comm.Logf("%s: directory", path)
		return nil
	}

	if stats.Size() == 0 {
		comm.Logf("%s: empty file. peaceful.", path)
		return nil
	}

	log := func(line string) {
		comm.Logf(line)
	}

	source := seeksource.FromFile(reader)

	_, err = source.Resume(nil)
	if err != nil {
		return errors.WithStack(err)
	}

	var magic int32
	err = binary.Read(source, wire.Endianness, &magic)
	if err != nil {
		return errors.Wrap(err, "reading magic number")
	}

	switch magic {
	case pwr.PatchMagic:
		{
			h := &pwr.PatchHeader{}
			rctx := wire.NewReadContext(source)
			err = rctx.ReadMessage(h)
			if err != nil {
				return errors.WithStack(err)
			}

			rctx, err = pwr.DecompressWire(rctx, h.GetCompression())
			if err != nil {
				return errors.WithStack(err)
			}
			container := &tlc.Container{}
			err = rctx.ReadMessage(container)
			if err != nil {
				return errors.WithStack(err)
			}

			log("pre-patch container:")
			container.Print(log)

			container.Reset()
			err = rctx.ReadMessage(container)
			if err != nil {
				return errors.WithStack(err)
			}

			log("================================")
			log("post-patch container:")
			container.Print(log)
		}

	case pwr.SignatureMagic:
		{
			h := &pwr.SignatureHeader{}
			rctx := wire.NewReadContext(source)
			err := rctx.ReadMessage(h)
			if err != nil {
				return errors.WithStack(err)
			}

			rctx, err = pwr.DecompressWire(rctx, h.GetCompression())
			if err != nil {
				return errors.WithStack(err)
			}
			container := &tlc.Container{}
			err = rctx.ReadMessage(container)
			if err != nil {
				return errors.WithStack(err)
			}
			container.Print(log)
		}

	case pwr.ManifestMagic:
		{
			h := &pwr.ManifestHeader{}
			rctx := wire.NewReadContext(source)
			err := rctx.ReadMessage(h)
			if err != nil {
				return errors.WithStack(err)
			}

			rctx, err = pwr.DecompressWire(rctx, h.GetCompression())
			if err != nil {
				return errors.WithStack(err)
			}

			container := &tlc.Container{}
			err = rctx.ReadMessage(container)
			if err != nil {
				return errors.WithStack(err)
			}
			container.Print(log)
		}

	case pwr.WoundsMagic:
		{
			wh := &pwr.WoundsHeader{}
			rctx := wire.NewReadContext(source)
			err := rctx.ReadMessage(wh)
			if err != nil {
				return errors.WithStack(err)
			}

			container := &tlc.Container{}
			err = rctx.ReadMessage(container)
			if err != nil {
				return errors.WithStack(err)
			}
			container.Print(log)

			for {
				wound := &pwr.Wound{}
				err = rctx.ReadMessage(wound)
				if err != nil {
					if errors.Cause(err) == io.EOF {
						break
					} else {
						return errors.WithStack(err)
					}
				}
				comm.Logf(wound.PrettyString(container))
			}
		}

	default:
		_, err := reader.Seek(0, io.SeekStart)
		if err != nil {
			return errors.WithStack(err)
		}

		wasZip := func() bool {
			zr, err := zip.NewReader(reader, stats.Size())
			if err != nil {
				if err != zip.ErrFormat {
					ctx.Must(err)
				}
				return false
			}

			container, err := tlc.WalkZip(zr, &tlc.WalkOpts{
				Filter: func(fi os.FileInfo) bool { return true },
			})
			ctx.Must(err)
			container.Print(log)
			return true
		}()

		if wasZip {
			return nil
		}

		wasTar := func() bool {
			tr := tar.NewReader(reader)

			for {
				hdr, err := tr.Next()
				if err != nil {
					if err == io.EOF {
						break
					}
					return false
				}

				comm.Logf("%s %10s %s", os.FileMode(hdr.Mode), progress.FormatBytes(hdr.Size), hdr.Name)
			}
			return true
		}()

		if wasTar {
			return nil
		}

		comm.Logf("%s: not able to list contents", path)
	}

	return nil
}
