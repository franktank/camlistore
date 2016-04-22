// +build ignore

/*
Copyright 2015 The Camlistore Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command build-camlistore-server builds camlistored and bundles all the
// necessary resources for a Camlistore server in docker. It should be run in a
// docker container.
package main // import "camlistore.org/misc/docker/server"

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

var (
	flagRev = flag.String("rev", "", "Camlistore revision to build (tag or commit hash). For development purposes, you can instead specify the path to a local Camlistore source tree from which to build, with the form \"WIP:/path/to/dir\".")
	outDir  = flag.String("outdir", "/OUT/", "Output directory, where camlistored and all the resources will be written")
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "%s --rev=camlistore_revision\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "%s --rev=WIP:/path/to/camli/source/dir\n", os.Args[0])
	flag.PrintDefaults()
	example(os.Args[0])
	os.Exit(1)
}

func example(program string) {
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "\tdocker run --rm --volume=/tmp/camli-build/camlistore.org:/OUT camlistore/go %s --rev=4e8413c5012c\n", program)
	fmt.Fprintf(os.Stderr, "\tdocker run --rm --volume=/tmp/camli-build/camlistore.org:/OUT --volume=~/camlistore.org:/IN camlistore/go %s --rev=WIP:/IN\n", program)
}

func isWIP() bool {
	return strings.HasPrefix(*flagRev, "WIP")
}

// localCamliSource returns the path to the local Camlistore source tree
// that should be specified in *flagRev if *flagRev starts with "WIP:",
// empty string otherwise.
func localCamliSource() string {
	if !isWIP() {
		return ""
	}
	return strings.TrimPrefix(*flagRev, "WIP:")
}

func rev() string {
	if isWIP() {
		return "WORKINPROGRESS"
	}
	return *flagRev
}

func getCamliSrc() {
	if localCamliSource() != "" {
		mirrorCamliSrc(localCamliSource())
		return
	}
	fetchCamliSrc()
}

func mirrorCamliSrc(srcDir string) {
	check(os.MkdirAll("/gopath/src", 0777))
	cmd := exec.Command("cp", "-a", srcDir, "/gopath/src/camlistore.org")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error mirroring camlistore source from %v: %v", srcDir, err)
	}
}

func fetchCamliSrc() {
	check(os.MkdirAll("/gopath/src/camlistore.org", 0777))
	check(os.Chdir("/gopath/src/camlistore.org"))

	res, err := http.Get("https://camlistore.googlesource.com/camlistore/+archive/" + *flagRev + ".tar.gz")
	check(err)
	defer res.Body.Close()
	gz, err := gzip.NewReader(res.Body)
	check(err)
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		check(err)
		if h.Typeflag == tar.TypeDir {
			check(os.MkdirAll(h.Name, os.FileMode(h.Mode)))
			continue
		}
		f, err := os.Create(h.Name)
		check(err)
		n, err := io.Copy(f, tr)
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		if n != h.Size {
			log.Fatalf("Error when creating %v: wanted %v bytes, got %v bytes", h.Name, h.Size, n)
		}
		check(f.Close())
	}
}

func buildCamlistored() {
	check(os.MkdirAll(path.Join(*outDir, "/bin"), 0777))
	check(os.MkdirAll(path.Join(*outDir, "/server/camlistored"), 0777))
	oldPath := os.Getenv("PATH")
	os.Setenv("GOPATH", "/gopath")
	os.Setenv("PATH", "/usr/local/go/bin:"+oldPath)
	os.Setenv("CGO_ENABLED", "0")
	os.Setenv("GO15VENDOREXPERIMENT", "1")
	cmd := exec.Command("go", "build",
		"-o", path.Join(*outDir, "/bin/camlistored"),
		`--ldflags`, "-w -d -linkmode internal -X camlistore.org/pkg/buildinfo.GitInfo="+rev(),
		"--tags=netgo", "camlistore.org/server/camlistored")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error building camlistored in go container: %v", err)
	}
}

func setUIResources() {
	cmd := exec.Command("mv", "/gopath/src/camlistore.org/server/camlistored/ui", path.Join(*outDir, "/server/camlistored/ui"))
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error moving UI dir %v in output dir %v: %v",
			"/gopath/src/camlistore.org/server/camlistored/ui", path.Join(*outDir, "/server/camlistored/ui"), err)
	}
	filepath.Walk("/gopath/src/camlistore.org/vendor/embed", func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			log.Fatalf("Error stating while cleaning %s: %v", path, err)
		}
		if fi.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			check(os.Remove(path))
		}
		return nil
	})
	check(os.MkdirAll(path.Join(*outDir, "/vendor"), 0777))
	cmd = exec.Command("mv", "/gopath/src/camlistore.org/vendor/embed", path.Join(*outDir, "/vendor/embed"))
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error moving vendor/embed dir %v in output dir %v: %v",
			"/gopath/src/camlistore.org/vendor/embed", path.Join(*outDir, "/vendor/embed"), err)
	}
}

func checkArgs() {
	if flag.NArg() != 0 {
		usage()
	}
	if *flagRev == "" {
		fmt.Fprintf(os.Stderr, "Usage error: --rev is required.\n")
		usage()
	}
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if _, err := os.Stat("/.dockerinit"); err != nil {
		fmt.Fprintf(os.Stderr, "Usage error: this program should be run within a docker container, and is meant to be called from misc/docker/dock.go\n")
		usage()
	}
	checkArgs()

	getCamliSrc()
	buildCamlistored()
	setUIResources()
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
