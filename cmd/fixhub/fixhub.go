/*
Fixhub tries to fix a GitHub repository containing Go source files.
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"code.google.com/p/goauth2/oauth"
	"github.com/dsymonds/fixhub"
)

var (
	personalAccessTokenFile = flag.String("personal_access_token_file", filepath.Join(os.Getenv("HOME"), ".fixhub-token"), "a file to load a GitHub personal access token from")
	rev                     = flag.String("rev", "master", "revision of the repo to check")
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

	var httpClient *http.Client
	if accessToken != "" {
		httpClient = (&oauth.Transport{
			Token: &oauth.Token{
				AccessToken: accessToken,
			},
		}).Client()
	}

	client, err := fixhub.NewClient(owner, repo, httpClient)
	if err != nil {
		log.Fatal(err)
	}

	ps, err := client.Check(*rev)
	if err != nil {
		log.Fatalf("Checking: %v", err)
	}

	sort.Sort(ps)
	for _, p := range ps {
		fmt.Println(p)
	}
	log.Printf("wow, there were %d problems!", len(ps))
}

/*
	http.Redirect(w, r, config.AuthCodeURL("foo"), http.StatusFound)
*/
