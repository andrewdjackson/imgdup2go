package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nr90/imgsim"
	"github.com/rif/imgdup2go/hasher"
	"github.com/rivo/duplo"
	"github.com/vbauerster/mpb"
	"github.com/vbauerster/mpb/decor"
)

var (
	extensions   = map[string]func(io.Reader) (image.Image, error){"jpg": jpeg.Decode, "jpeg": jpeg.Decode, "png": png.Decode, "gif": gif.Decode}
	dst          = "duplicates"
	keepPrefix   = "_KEPT_"
	deletePrefix = "_GONE_"
	algo         = flag.String("algo", "avg", "algorithm for image hashing fmiq|avg|diff")
	sensitivity  = flag.Int("sensitivity", 0, "the sensitivity treshold (the lower, the better the match (can be negative)) - fmiq algorithm only")
	path         = flag.String("path", ".", "the path to search the images")
	dryRun       = flag.Bool("dryrun", false, "only print found matches")
	undo         = flag.Bool("undo", false, "restore removed duplicates")
)

type imgInfo struct {
	fileInfo os.FileInfo
	path     string
	res      int
}

// CopyFile copies a file from src to dst. If src and dst files exist, and are
// the same, then return success. Otherise, attempt to create a hard link
// between the two files. If that fail, copy the file contents from src to dst.
func CopyFile(src, dst string) (err error) {
	sfi, err := os.Stat(src)
	if err != nil {
		return
	}
	if !sfi.Mode().IsRegular() {
		// cannot copy non-regular files (e.g., directories,
		// symlinks, devices, etc.)
		return fmt.Errorf("CopyFile: non-regular source file %s (%q)", sfi.Name(), sfi.Mode().String())
	}
	dfi, err := os.Stat(dst)
	if err != nil {
		if !os.IsNotExist(err) {
			return
		}
	} else {
		if !(dfi.Mode().IsRegular()) {
			return fmt.Errorf("CopyFile: non-regular destination file %s (%q)", dfi.Name(), dfi.Mode().String())
		}
		if os.SameFile(sfi, dfi) {
			return
		}
	}
	if err = os.Link(src, dst); err == nil {
		return
	}
	err = copyFileContents(src, dst)
	return
}

// copyFileContents copies the contents of the file named src to the file named
// by dst. The file will be created if it does not already exist. If the
// destination file exists, all it's contents will be replaced by the contents
// of the source file.
func copyFileContents(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return
	}
	err = out.Sync()
	return
}

func main() {
	flag.Parse()

	var buf bytes.Buffer
	logger := log.New(&buf, "logger: ", log.Lshortfile)

	*sensitivity -= 100

	if *undo {
		undoDelete(logger)
	} else {
		files := getAllFiles(*path)
		// Create an empty store.
		var store hasher.Store
		switch *algo {
		case "fmiq":
			store = hasher.NewDuploStore(*sensitivity)
		default:
			store = hasher.NewImgsimStore()
		}
		logger.Printf("Found %d files\n", len(files))

		p, bar := createProgressBar(files)
		processFiles(files, bar, logger, store)
		p.Wait()
	}

	fmt.Print("Report:\n", &buf)
}

func createProgressBar(files []imgInfo) (*mpb.Progress, *mpb.Bar) {
	p := mpb.New(
		// override default (80) width
		mpb.WithWidth(64),
		// override default "[=>-]" format
		mpb.WithFormat("╢▌▌░╟"),
		// override default 120ms refresh rate
		mpb.WithRefreshRate(180*time.Millisecond),
	)

	name := "Processed Images:"
	// Add a bar
	// You're not limited to just a single bar, add as many as you need
	bar := p.AddBar(int64(len(files)),
		// Prepending decorators
		mpb.PrependDecorators(
			// display our name with one space on the right
			decor.Name(name, decor.WC{W: len(name) + 1, C: decor.DidentRight}),
			decor.OnComplete(
				// ETA decorator with ewma age of 60, and width reservation of 4
				decor.EwmaETA(decor.ET_STYLE_GO, 60, decor.WC{W: 4}), "done",
			),
		),
		// Appending decorators
		mpb.AppendDecorators(
			// Percentage decorator with minWidth and no extra config
			decor.Percentage(),
		),
	)
	return p, bar
}

func undoDelete(logger *log.Logger) {
	dst = filepath.Join(*path, dst)
	files := getAllFiles(dst)

	for _, f := range files {
		if strings.Contains(f.fileInfo.Name(), keepPrefix) {
			if *dryRun {
				logger.Println("removing ", f.path)
			} else {
				os.Remove(f.path)
			}
		}
		if strings.Contains(f.fileInfo.Name(), deletePrefix) {
			if *dryRun {
				logger.Printf("moving %s to %s\n ", f.path, f.path[13:])
			} else {
				os.Rename(f.path, f.path[13:])
			}
		}
	}
	if *dryRun {
		logger.Print("removing directory: ", dst)
	} else {
		if err := os.Remove(dst); err != nil {
			logger.Print("could not remove duplicates folder: ", err)
		}
	}
	os.Exit(0)
}

func getAllFiles(path string) []imgInfo {
	var f []imgInfo

	e := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			if !info.IsDir() {
				println(path)
				i := imgInfo{
					fileInfo: info,
					path:     path,
				}
				f = append(f, i)
			}
		}
		return nil
	})
	if e != nil {
		log.Fatal(e)
	}

	return f
}

func processFiles(files []imgInfo, bar *mpb.Bar, logger *log.Logger, store hasher.Store) {
	for _, f := range files {
		processFile(f, logger, store)
		bar.Increment()
	}
}

func processFile(f imgInfo, logger *log.Logger, store hasher.Store) bool {
	processed := false

	// get file extension
	ext := filepath.Ext(f.fileInfo.Name())
	if len(ext) > 1 {
		ext = ext[1:]
	}

	// process valid extension
	if _, ok := extensions[ext]; ok {
		//fn := filepath.Join(*path, f.fileInfo.Name())
		fn := f.path

		if file, err := os.Open(fn); err == nil {
			if _, format, err := image.DecodeConfig(file); err == nil {
				file.Close()

				if decodeFunc, ok := extensions[format]; ok {
					if file, err := os.Open(fn); err == nil {
						if img, err := decodeFunc(file); err == nil {
							b := img.Bounds()
							res := b.Dx() * b.Dy()
							// Add image "img" to the store.
							var hash interface{}
							switch *algo {
							case "fmiq":
								hash, _ = duplo.CreateHash(img)
							case "avg":
								hash = imgsim.AverageHash(img)
							case "diff":
								hash = imgsim.DifferenceHash(img)
							default:
								hash = imgsim.AverageHash(img)
							}
							match := store.Query(hash)
							if match != nil {
								ii := match.(*imgInfo)
								fi := ii.fileInfo
								logger.Printf("%s matches: %s\n", fn, fi.Name())

								if !*dryRun {
									_, err := os.Stat(dst)
									if err != nil && os.IsNotExist(err) {
										if err := os.Mkdir(dst, os.ModePerm); err != nil {
											logger.Println("Could not create destination directory: ", err)
											os.Exit(1)
										}
									}

									hasher := md5.New()
									hasher.Write([]byte(f.fileInfo.Name() + fi.Name()))
									sum := hex.EncodeToString(hasher.Sum(nil))[:5]
									if res > ii.res {
										store.Add(&imgInfo{fileInfo: f.fileInfo, res: res}, hash)
										store.Delete(fi, hash)
										if err := os.Rename(filepath.Join(*path, fi.Name()), filepath.Join(dst, fmt.Sprintf("%s_%s_%s", sum, deletePrefix, fi.Name()))); err != nil {
											logger.Println("error moving file: " + fmt.Sprintf("%s_%s_%s", sum, deletePrefix, fi.Name()))
										}
										if err := CopyFile(filepath.Join(*path, f.fileInfo.Name()), filepath.Join(dst, fmt.Sprintf("%s_%s_%s", sum, keepPrefix, f.fileInfo.Name()))); err != nil {
											logger.Println("error copying file: " + fmt.Sprintf("%s_%s_%s", sum, keepPrefix, f.fileInfo.Name()))
										}
									} else {
										if err := CopyFile(filepath.Join(*path, fi.Name()), filepath.Join(dst, fmt.Sprintf("%s_%s_%s", sum, keepPrefix, fi.Name()))); err != nil {
											logger.Println("error copying file: " + fmt.Sprintf("%s_%s_%s", sum, keepPrefix, fi.Name()))
										}
										if err := os.Rename(filepath.Join(*path, f.fileInfo.Name()), filepath.Join(dst, fmt.Sprintf("%s_%s_%s", sum, deletePrefix, f.fileInfo.Name()))); err != nil {
											logger.Println("error moving file: " + fmt.Sprintf("%s_%s_%s", sum, deletePrefix, f.fileInfo.Name()))
										}
									}
								} else {
									store.Add(&imgInfo{fileInfo: f.fileInfo, res: res}, hash)
								}
							} else {
								store.Add(&imgInfo{fileInfo: f.fileInfo, res: res}, hash)
							}
							if err := file.Close(); err != nil {
								logger.Println("could not close file: ", fn)
							}
						} else {
							logger.Printf("ignoring %s: %v\n", fn, err)
						}
					} else {
						logger.Printf("%s: %v\n", fn, err)
					}
				}
			} else {
				logger.Printf("%s: %v\n", fn, err)
				file.Close()
			}

		} else {
			logger.Printf("%s: %v\n", fn, err)
		}
	}

	return processed
}
