/*
Package fixhub implements infrastructure for checking GitHub repositories
containing Go source files, and generating fixes for the problems.
*/
package fixhub

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"go/format"
	"go/scanner"
	"net/http"
	"sort"
	"strings"
	"sync"

	"code.google.com/p/goauth2/oauth"
	"github.com/golang/lint"
	"github.com/google/go-github/github"
)

const (
	// sizeLimit is the largest file to fetch.
	sizeLimit = 1 << 20 // 1 MB
)

// Client is a client for interacting with GitHub repositories.
type Client struct {
	gc          *github.Client
	owner, repo string

	FetchParallelism int // max fetches to do at once in an operation
}

// NewClient returns a new client.
// If accessToken is empty then the client will be unauthenticated.
func NewClient(owner, repo, accessToken string) (*Client, error) {
	// Either load the personal access token (and set httpClient accordingly),
	// or leave httpClient as nil to get an unauthenticated client.
	var httpClient *http.Client
	if accessToken != "" {
		httpClient = (&oauth.Transport{
			Token: &oauth.Token{
				AccessToken: accessToken,
			},
		}).Client()
	}

	gc := github.NewClient(httpClient)
	gc.UserAgent = "fixhub"

	return &Client{
		gc:    gc,
		owner: owner,
		repo:  repo,

		FetchParallelism: 10,
	}, nil
}

// ResolveRef resolves the given ref into the SHA-1 commit ID.
func (c *Client) ResolveRef(ref string) (sha1 string, err error) {
	commit, _, err := c.gc.Repositories.GetCommit(c.owner, c.repo, ref)
	if err != nil {
		return "", err
	}
	return *commit.SHA, nil
}

// GetTree fetches the github tree by SHA-1 commit ID.
func (c *Client) GetTree(sha1 string) (*github.Tree, error) {
	tree, _, err := c.gc.Git.GetTree(c.owner, c.repo, sha1, true)
	return tree, err
}

// GetBlob fetches the repository blob by SHA-1 ID.
func (c *Client) GetBlob(sha1 string) ([]byte, error) {
	blob, _, err := c.gc.Git.GetBlob(c.owner, c.repo, sha1)
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

// A Problem is something that was found wrong.
type Problem struct {
	File string
	Line int    // line number, starting at 1
	Text string // the prose that describes the problem
}

func (p Problem) String() string {
	return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Text)
}

// Problems is a slice of Problem.
// It satisfies sort.Interface.
type Problems []Problem

func (ps Problems) Len() int      { return len(ps) }
func (ps Problems) Swap(i, j int) { ps[i], ps[j] = ps[j], ps[i] }
func (ps Problems) Less(i, j int) bool {
	if a, b := ps[i].File, ps[j].File; a != b {
		return a < b
	}
	if a, b := ps[i].Line, ps[j].Line; a != b {
		return a < b
	}
	return ps[i].Text < ps[j].Text
}

// Check runs checks on the Go source files at the named revision.
func (c *Client) Check(rev string) (Problems, error) {
	ref, err := c.ResolveRef(rev) // TODO: skip this if it looks like a SHA-1 hash
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %v", rev, err)
	}
	tree, err := c.GetTree(ref)
	if err != nil {
		return nil, fmt.Errorf("fetching tree %q (%s): %v", rev, ref, err)
	}

	// TODO: do more than lint

	var (
		linter = new(lint.Linter)
		sem    = make(chan int, c.FetchParallelism)

		wg       sync.WaitGroup
		problems struct {
			sync.Mutex
			list []Problem
		}
	)
	addProblem := func(p Problem) {
		problems.Lock()
		problems.list = append(problems.list, p)
		problems.Unlock()
	}
	addScannerError := func(path string, err *scanner.Error) {
		addProblem(Problem{
			File: path,
			Line: err.Pos.Line,
			Text: err.Msg,
		})
	}

	for _, ent := range tree.Entries {
		if ent.SHA == nil || ent.Path == nil || ent.Size == nil {
			continue
		}
		sha1, path, size := *ent.SHA, *ent.Path, *ent.Size
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if strings.HasSuffix(path, ".pb.go") {
			continue
		}
		if size > sizeLimit {
			//log.Printf("Skipping %s because it is too big: %d > %d", path, size, sizeLimit)
			continue
		}
		//log.Printf("+ %s (%d bytes)", path, size)

		wg.Add(1)
		go func() {
			defer wg.Done()

			// TODO: figure out how to do error reporting in here

			sem <- 1
			src, err := c.GetBlob(sha1)
			<-sem
			if err != nil {
				//log.Printf("Getting blob for %s: %v", path, err)
				return
			}

			formatted, err := format.Source(src)
			if err != nil {
				switch err := err.(type) {
				case scanner.ErrorList:
					for _, err := range err {
						addScannerError(path, err)
					}
				case *scanner.Error:
					addScannerError(path, err)
				default:
					addProblem(Problem{
						File: path,
						Text: err.Error(),
					})
				}
				return // no more to do if we have syntax errors
			}
			if !bytes.Equal(src, formatted) {
				addProblem(Problem{
					File: path,
					Text: "This file needs formatting with gofmt.",
				})
			}

			ps, err := linter.Lint(path, src)
			if err != nil {
				//log.Printf("Linting %s: %v", path, err)
				return
			}
			for _, p := range ps {
				if p.Confidence < 0.8 { // TODO: flag
					continue
				}
				addProblem(Problem{
					File: path,
					Line: p.Position.Line,
					Text: p.Text,
				})
			}
		}()
	}
	wg.Wait()
	sort.Sort(Problems(problems.list))
	return problems.list, nil
}
