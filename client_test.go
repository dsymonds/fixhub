package fixhub

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/github"
)

// NOTE(dsymonds): This test is a bit pointless since it mainly tests the
// go-github package, but it exercises the fake and it will be more interesting
// when the client itself does linting and vetting.

func TestBasic(t *testing.T) {
	c, cleanup := newFakeClient(t)
	defer cleanup()

	master, err := c.ResolveRef("master")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	tree, err := c.GetTree(master)
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}

	// Find "p1.go" and check we can get its contents.
	var sha1 string
	for _, ent := range tree.Entries {
		if *ent.Path == "p1.go" {
			sha1 = *ent.SHA
			break
		}
	}
	if sha1 == "" {
		t.Fatal("Didn't find p1.go")
	}
	data, err := c.GetBlob(sha1)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if !bytes.Contains(data, []byte("package proj")) {
		t.Fatalf("Bad blob: %q", data)
	}
}

func newFakeClient(t *testing.T) (client *Client, cleanup func()) {
	const owner, proj = "faker", "proj"

	f, err := newFakeGitHub(filepath.Join("testdata", owner, proj))
	if err != nil {
		t.Fatalf("newFakeGitHub: %v", err)
	}

	c, err := NewClient(owner, proj, "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	srv := httptest.NewServer(f)
	c.gc.BaseURL, err = url.Parse(srv.URL + "/gh/")
	if err != nil {
		srv.Close()
		t.Fatalf("Bad httptest address %q: %v", srv.URL, err)
	}
	return c, srv.Close
}

type fakeGitHub struct {
	baseDir string

	master string // SHA-1

	files map[string]string // path -> SHA-1
	blobs map[string][]byte // SHA-1 -> content
}

func newFakeGitHub(baseDir string) (*fakeGitHub, error) {
	f := &fakeGitHub{
		baseDir: baseDir,
		master:  "deadbeef",
		files:   make(map[string]string),
		blobs:   make(map[string][]byte),
	}

	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path) // can't fail
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		sha1 := fmt.Sprintf("%02x", sha1.Sum(data))
		f.files[rel] = sha1
		f.blobs[sha1] = data
		return nil
	})
	return f, err
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/gh/repos/faker/proj")
	if path == r.URL.Path {
		// didn't have prefix
		http.Error(w, "bad path", http.StatusForbidden)
		return
	}

	switch path {
	case "/commits/master":
		writeJSON(w, &github.RepositoryCommit{SHA: &f.master})
		return
	case "/git/trees/" + f.master: // assume "?recursive=1":
		t := &github.Tree{SHA: &f.master}
		for path, sha1 := range f.files {
			t.Entries = append(t.Entries, github.TreeEntry{
				SHA:  github.String(sha1),
				Path: github.String(path),
				Size: github.Int(len(f.blobs[sha1])),
			})
		}
		writeJSON(w, t)
		return
	}

	if sha1 := strings.TrimPrefix(path, "/git/blobs/"); sha1 != path {
		data := f.blobs[sha1]
		if data == nil {
			http.Error(w, "no such blob "+sha1, 404)
			return
		}
		writeJSON(w, &github.Blob{
			Content:  github.String(base64.StdEncoding.EncodeToString(data)),
			Encoding: github.String("base64"),
			Size:     github.Int(len(data)),
		})
		return
	}

	log.Printf("r: %v", r)
	w.WriteHeader(http.StatusTeapot)
}

func writeJSON(w http.ResponseWriter, obj interface{}) {
	b, err := json.Marshal(obj)
	if err != nil {
		http.Error(w, fmt.Sprintf("internal JSON error: %v", err), 500)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.github.v3+json")
	w.Write(b)
}
