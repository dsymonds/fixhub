/*
Fixhub runs golint on a GitHub repository.
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dsymonds/fixhub/fixhub"
	"github.com/golang/lint"
)

var (
	personalAccessTokenFile = flag.String("personal_access_token_file", filepath.Join(os.Getenv("HOME"), ".fixhub-token"), "a file to load a GitHub personal access token from")
	rev                     = flag.String("rev", "master", "revision of the repo to check")
)

const (
	sizeLimit = 1 << 20 // 1 MB
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: fixhub [options] owner/repo")
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

	var accessToken string
	if pat, err := ioutil.ReadFile(*personalAccessTokenFile); err == nil {
		// security check
		fi, err := os.Stat(*personalAccessTokenFile)
		if err != nil {
			log.Fatalf("os.Stat(%q): %v", *personalAccessTokenFile, err)
		}
		if fi.Mode()&0077 != 0 { // check that no group/world perm bits are set
			log.Fatalf("%s is too accessible; run `chmod go= %s` to fix", *personalAccessTokenFile, *personalAccessTokenFile)
		}

		accessToken = string(bytes.TrimSpace(pat))
	}

	client, err := fixhub.NewClient(owner, repo, accessToken)
	if err != nil {
		log.Fatal(err)
	}

	ref, err := client.ResolveRef(*rev)
	if err != nil {
		log.Fatalf("GetCommit(%q): %v", *rev, err)
	}
	log.Printf("rev %q is %s", *rev, ref)

	tree, err := client.GetTree(ref)
	if err != nil {
		log.Fatalf("GetTree: %v", err)
	}
	log.Printf("Found %d tree entries", len(tree.Entries))

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

			src, err := client.GetBlob(sha1)
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
