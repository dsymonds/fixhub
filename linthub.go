/*
Linthub runs golint on a GitHub repository.
*/
package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"code.google.com/p/goauth2/oauth"
	"github.com/golang/lint"
	"github.com/google/go-github/github"
)

var (
	personalAccessTokenFile = flag.String("personal_access_token_file", filepath.Join(os.Getenv("HOME"), ".linthub-token"), "a file to load a GitHub personal access token from")
	rev                     = flag.String("rev", "master", "revision of the repo to check")
)

const (
	sizeLimit = 1 << 20 // 1 MB
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: linthub [options] owner/repo")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}
	parts := strings.Split(flag.Arg(0), "/")
	if len(parts) != 2 {
		flag.Usage()
		os.Exit(1)
	}
	owner, repo := parts[0], parts[1]

	// Either load the personal access token (and set httpClient accordingly),
	// or leave httpClient as nil to get an unauthenticated client.
	var httpClient *http.Client
	pat, err := ioutil.ReadFile(*personalAccessTokenFile)
	if err == nil {
		// security check
		fi, err := os.Stat(*personalAccessTokenFile)
		if err != nil {
			log.Fatalf("os.Stat(%q): %v", *personalAccessTokenFile, err)
		}
		if fi.Mode()&0077 != 0 { // check that no group/world perm bits are set
			log.Fatalf("%s is too accessible; run `chmod go= %s` to fix", *personalAccessTokenFile, *personalAccessTokenFile)
		}

		tr := &oauth.Transport{
			Token: &oauth.Token{
				AccessToken: string(bytes.TrimSpace(pat)),
			},
		}
		httpClient = tr.Client()
	}

	client := github.NewClient(httpClient)
	client.UserAgent = "linthub"

	commit, _, err := client.Repositories.GetCommit(owner, repo, *rev)
	if err != nil {
		log.Fatalf("GetCommit(%q, %q, %q): %v", owner, repo, *rev, err)
	}
	sha1 := *commit.SHA
	log.Printf("%s/%s: rev %q is %s", owner, repo, *rev, sha1)

	tree, _, err := client.Git.GetTree(owner, repo, sha1, true)
	if err != nil {
		log.Fatalf("GetTree: %v", err)
	}
	log.Printf("%s/%s: found %d tree entries", owner, repo, len(tree.Entries))

	var (
		linter = new(lint.Linter)

		wg       sync.WaitGroup
		problems struct {
			sync.Mutex
			list []string
		}
	)
	addProblem := func(s string) {
		problems.Lock()
		problems.list = append(problems.list, s)
		problems.Unlock()
	}

	nGo := 0
	for _, ent := range tree.Entries {
		if ent.SHA == nil || ent.Path == nil || ent.Size == nil {
			continue
		}
		sha1, path, size := *ent.SHA, *ent.Path, *ent.Size
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if size > sizeLimit {
			log.Printf("Skipping %s because it is too big: %d > %d", path, size, sizeLimit)
			continue
		}
		//log.Printf("+ %s (%d bytes)", path, size)

		wg.Add(1)
		nGo++
		go func() {
			defer wg.Done()

			src, err := getBlob(client, owner, repo, sha1)
			if err != nil {
				log.Printf("Getting blob for %s: %v", path, err)
				return
			}
			ps, err := linter.Lint(path, src)
			if err != nil {
				log.Printf("Linting %s: %v", path, err)
				return
			}
			for _, p := range ps {
				if p.Confidence < 0.8 { // TODO: flag
					continue
				}
				addProblem(fmt.Sprintf("%s:%v: %s", path, p.Position, p.Text))
			}
		}()
	}
	wg.Wait()

	sort.Strings(problems.list)
	for _, p := range problems.list {
		fmt.Println(p)
	}
	log.Printf("wow, there were %d problems in %d Go source files!", len(problems.list), nGo)
}

func getBlob(client *github.Client, owner, repo, sha1 string) ([]byte, error) {
	blob, _, err := client.Git.GetBlob(owner, repo, sha1)
	if err != nil {
		return nil, err
	}
	content := *blob.Content
	switch *blob.Encoding {
	case "base64":
		return base64.StdEncoding.DecodeString(content)
	default:
		return nil, fmt.Errorf("unknown blob encoding %q", *blob.Encoding)
	}
}
