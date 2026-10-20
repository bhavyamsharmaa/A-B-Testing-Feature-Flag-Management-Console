// Command imagecheck inspects the files of a container image (an extracted root filesystem, or the tar stream
// `docker export` produces) and fails if it holds anything that must not ship: environment files, keys and credentials,
// source code, tests and test data, or anything outside the short list of places the image is allowed to use.
//
//	docker export $(docker create IMAGE) | go run ./tools/imagecheck -tar -
//	go run ./tools/imagecheck -rootfs ~/helios-local/image-stage/rootfs
//
// It prints the files with their sizes and the total, then one line per finding, and exits 1 if there is any.
package main

import (
	"archive/tar"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Allowed is where an image may have files at all. The image is `FROM scratch`: its only legitimate content is the
// application, the CA certificates, and the two files that name the unprivileged user.
var Allowed = []string{"/app/", "/etc/passwd", "/etc/group", "/etc/ssl/certs/ca-certificates.crt"}

var forbiddenName = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)\.env(\..*)?$`),
	regexp.MustCompile(`(^|/)secrets?\.(env|json|ya?ml|txt)$`),
	regexp.MustCompile(`\.(pem|key|p12|pfx|jks|kdbx)$`),
	regexp.MustCompile(`(^|/)id_(rsa|dsa|ecdsa|ed25519)`),
	regexp.MustCompile(`(^|/)\.(ssh|aws|gnupg|docker|kube|git)(/|$)`),
	regexp.MustCompile(`(^|/)\.(npmrc|netrc|pgpass|git-credentials)$`),
	regexp.MustCompile(`(^|/)credentials(\.[a-z]+)?$`),
}

var sourceOrTest = []*regexp.Regexp{
	regexp.MustCompile(`\.go$`), regexp.MustCompile(`(^|/)go\.(mod|sum)$`),
	regexp.MustCompile(`(^|/)testdata(/|$)`), regexp.MustCompile(`_test\.go$`), regexp.MustCompile(`\.golden$`),
	regexp.MustCompile(`(^|/)node_modules(/|$)`), regexp.MustCompile(`\.(tsx?|jsx?|py)$`), regexp.MustCompile(`\.sql$`),
}

// secretText finds credentials inside files. A Go binary is scanned too: nothing in it should look like one.
var secretText = []struct {
	why string
	re  *regexp.Regexp
}{
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"a database URL with a password", regexp.MustCompile(`postgres(ql)?://[^:@/\s"']+:[^@\s"']{3,}@`)},
	{"a Redis URL with a password", regexp.MustCompile(`rediss?://[^:@/\s"']*:[^@\s"']{3,}@`)},
	{"an AWS access key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"a Helios SDK or API key", regexp.MustCompile(`hlt_(sdk|server|tok)_[A-Za-z0-9_]{16,}`)},
	{"a webhook signing secret", regexp.MustCompile(`whsec_[A-Za-z0-9+/=_-]{16,}`)},
	{"a JSON web token", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{15,}\.eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{10,}`)},
	{"a live payment key", regexp.MustCompile(`sk_live_[A-Za-z0-9]{16,}`)},
}

// File is one entry of the image.
type File struct {
	Path string // absolute, slash-separated, as in the image
	Size int64
	Mode fs.FileMode
	Body []byte // content, for files up to MaxScan bytes; nil for larger ones
}

// MaxScan is the largest file whose content is read for secrets.
const MaxScan = 64 << 20

type Finding struct{ Path, Why string }

// Check applies every rule to the files.
func Check(files []File) (findings []Finding, total int64) {
	for _, f := range files {
		total += f.Size
		p := f.Path
		if f.Mode.IsDir() {
			continue
		}
		allowed := false
		for _, a := range Allowed {
			if p == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(p, a)) {
				allowed = true
			}
		}
		if !allowed {
			findings = append(findings, Finding{p, "outside the places the image may use (" + strings.Join(Allowed, ", ") + ")"})
		}
		for _, re := range forbiddenName {
			if re.MatchString(p) {
				findings = append(findings, Finding{p, "an environment, key or credentials file"})
			}
		}
		for _, re := range sourceOrTest {
			if re.MatchString(p) {
				findings = append(findings, Finding{p, "source, a test or test data"})
			}
		}
		if f.Mode&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
			findings = append(findings, Finding{p, "setuid or setgid"})
		}
		if f.Mode.Perm()&0o002 != 0 {
			findings = append(findings, Finding{p, "writable by everyone"})
		}
		for _, s := range secretText {
			if s.re.Match(f.Body) {
				findings = append(findings, Finding{p, "contains " + s.why})
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Why < findings[j].Why
	})
	return findings, total
}

// FromRootfs reads an extracted root filesystem.
func FromRootfs(root string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		f := File{Path: "/" + filepath.ToSlash(rel), Size: info.Size(), Mode: info.Mode()}
		if info.Mode().IsRegular() && info.Size() <= MaxScan {
			if f.Body, err = os.ReadFile(p); err != nil {
				return err
			}
		}
		out = append(out, f)
		return nil
	})
	return out, err
}

// FromTar reads a tar stream (docker export).
func FromTar(r io.Reader) ([]File, error) {
	var out []File
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		f := File{Path: path.Clean("/" + h.Name), Size: h.Size, Mode: h.FileInfo().Mode()}
		if h.Typeflag == tar.TypeReg && h.Size <= MaxScan {
			var b bytes.Buffer
			if _, err := io.Copy(&b, tr); err != nil {
				return nil, err
			}
			f.Body = b.Bytes()
		}
		out = append(out, f)
	}
}

func main() {
	rootfs := flag.String("rootfs", "", "an extracted root filesystem directory")
	tarFile := flag.String("tar", "", "a tar file or - for stdin (docker export)")
	quiet := flag.Bool("q", false, "do not list the files")
	flag.Parse()
	var files []File
	var err error
	switch {
	case *rootfs != "":
		files, err = FromRootfs(*rootfs)
	case *tarFile == "-":
		files, err = FromTar(os.Stdin)
	case *tarFile != "":
		var f *os.File
		if f, err = os.Open(*tarFile); err == nil {
			defer f.Close()
			files, err = FromTar(f)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: imagecheck -rootfs DIR | -tar FILE|-")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "imagecheck:", err)
		os.Exit(2)
	}
	findings, total := Check(files)
	if !*quiet {
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, f := range files {
			if !f.Mode.IsDir() {
				fmt.Printf("%10d  %s\n", f.Size, f.Path)
			}
		}
	}
	fmt.Printf("%d files, %.1f MiB\n", len(files), float64(total)/(1<<20))
	for _, f := range findings {
		fmt.Printf("FOUND %s: %s\n", f.Path, f.Why)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
	fmt.Println("clean: no environment files, keys, credentials, source, tests or test data, and nothing outside the allowed places")
}
