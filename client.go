/*
Package fixhub implements infrastructure for checking GitHub repositories
containing Go source files, and generating fixes for the problems.
*/
package fixhub

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"go/build"
	"go/format"
	"go/scanner"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	gc     *github.Client
	config *oauth.Config
	owner  string // owner of originally requested repository
	repo   string // originally requested repository

	workingOwner string // owner of repository to branch into
	workingRepo  string // repository to branch into (branch name: fixhub-user)

	user string // currently authenticated user (can vary from owner)

	FetchParallelism int    // max fetches to do at once in an operation
	ScratchDir       string // where we can scribble files; defaults to os.TempDir()

	// VetBinary is the path to vet.
	// If this is the empty string we try to find it under GOROOT.
	VetBinary string
}

// NewClient returns a new client.
// If accessToken is empty then the client will be unauthenticated.
func NewClient(owner, repo string, client *http.Client) (*Client, error) {
	gc := github.NewClient(client)
	gc.UserAgent = "fixhub"

	return &Client{
		gc:    gc,
		owner: owner,
		repo:  repo,

		FetchParallelism: 10,
	}, nil
}

func (c *Client) tempDir() string {
	if c.ScratchDir != "" {
		return c.ScratchDir
	}
	return os.TempDir()
}

func (c *Client) loadUser() error {
	user, _, err := c.gc.Users.Get("")
	if err != nil {
		return err
	}
	if user == nil || user.Login == nil || *user.Login == "" {
		return errors.New("no github login info")
	}
	c.user = *user.Login
	return nil
}

func (c *Client) loadWorkingRepo() error {
	// Owners and collaborators work on branches in the original repository.
	if c.user == c.owner {
		c.workingOwner = c.owner
		c.workingRepo = c.repo
		return nil
	}
	collaborator, _, err := c.gc.Repositories.IsCollaborator(c.owner, c.repo, c.user)
	if err != nil {
		return err
	}
	if collaborator {
		c.workingOwner = c.owner
		c.workingRepo = c.repo
		return nil
	}

	// No permission, fork for authenticated user.
	res, _, err := c.gc.Repositories.CreateFork(c.owner, c.repo, nil)
	if err != nil {
		return err
	}
	if res.Name == nil {
		return fmt.Errorf("fork of %s/%s returned no name", c.owner, c.repo)
	}
	c.workingOwner = c.user
	c.workingRepo = *res.Name

	// Repos are created asynchronously, so spin for a while until it appears.
	wait := 50 * time.Millisecond
	for wait < 5*time.Second {
		time.Sleep(wait)
		_, _, err := c.gc.Repositories.Get(c.workingOwner, c.workingRepo)
		if err == nil {
			break
		}
		wait *= 2
	}

	return nil
}

// Branch creates the branch fixhub-user if it does not exist.
// If the authenticated user != owner, it forks the current owner/repo.
func (c *Client) Branch() (name string, err error) {
	if err := c.loadUser(); err != nil {
		return "", err
	}
	if err := c.loadWorkingRepo(); err != nil {
		return "", err
	}

	branch := fmt.Sprintf("fixhub-%s", c.user)
	ref := "heads/" + branch
	res, _, err := c.gc.Git.GetRef(c.workingOwner, c.workingRepo, ref)
	if err == nil {
		log.Printf("GetRef(%s, %s, %s) no error: %+v", c.workingOwner, c.workingRepo, ref, res)
		// no need to make branch
		return branch, nil
	}

	// branch from master
	master, _, err := c.gc.Git.GetRef(c.workingOwner, c.workingRepo, "heads/master")
	if err != nil {
		return "", err
	}
	log.Printf("CreateRef(%s, %s ... %s)", c.workingOwner, c.workingRepo, ref)
	_, _, err = c.gc.Git.CreateRef(c.workingOwner, c.workingRepo, &github.Reference{
		Ref:    str(ref),
		Object: &github.GitObject{SHA: master.Object.SHA},
	})
	return branch, err
}

func str(s string) *string {
	return &s
}

// CommitFix commits the fix to a fixhub branch.
// The cmpURL result is a page where
func (c *Client) CommitFix(p Problem, f Fix) (cmpURL string, err error) {
	if c.user == "" {
		return "", errors.New("no authenticated user")
	}
	now := time.Now()
	branch := fmt.Sprintf("fixhub-%s", c.user)
	opt := &github.RepositoryContentFileOptions{
		Message: str("fixhub: " + string(p.Type)),
		Content: []byte(f.New),
		SHA:     str(p.SHA1),
		Branch:  str(branch),
		Author: &github.CommitAuthor{
			Date:  &now,
			Name:  str("fixhub"),
			Email: str("golang-nuts@googlegroups.com"),
		},
	}

	_, _, err = c.gc.Repositories.UpdateFile(c.workingOwner, c.workingRepo, p.File, opt)

	if c.workingOwner == c.owner && c.workingRepo == c.repo {
		cmpURL = fmt.Sprintf("https://github.com/%s/%s/compare/%s?expand=1", c.owner, c.repo, branch)
	} else {
		cmpURL = fmt.Sprintf("https://github.com/%s/%s/compare/%s:master...%s:fixhub-%s?expand=1", c.workingOwner, c.workingRepo, c.owner, c.workingOwner, c.user)
	}

	return cmpURL, err
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

type ProblemType string

const (
	Gofmt ProblemType = "gofmt"
)

// A Problem is something that was found wrong.
type Problem struct {
	Type    ProblemType
	File    string
	SHA1    string // blob SHA-1, always set if problem is Fixable
	Line    int    // line number, starting at 1
	Text    string // the prose that describes the problem
	Fixable bool
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

// A Fix is a fixed version of a file that was found wrong.
type Fix struct {
	Orig string
	New  string
}

// Fix fixes a problem.
func (c *Client) Fix(p Problem) (Fix, error) {
	if !p.Fixable {
		return Fix{}, fmt.Errorf("problem %q not fixable", p.Text)
	}
	// TODO: fix more problems
	if p.Type != Gofmt {
		return Fix{}, fmt.Errorf("do not know how to fix problem %q", p.Text)
	}

	orig, err := c.GetBlob(p.SHA1)
	if err != nil {
		return Fix{}, err
	}
	new, err := format.Source(orig)
	if err != nil {
		return Fix{}, err
	}
	return Fix{
		Orig: string(orig),
		New:  string(new),
	}, nil
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

	// Look for vet.
	vet := c.VetBinary
	if vet == "" {
		vet = filepath.Join(build.ToolDir, "vet")
		if _, err := os.Stat(vet); err != nil {
			// don't care what the error is; silently ignore vet
			log.Printf("XXX: vet stat: %v", err)
			vet = ""
		}
	}

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
					Type:    Gofmt,
					File:    path,
					Text:    "This file needs formatting with gofmt.",
					SHA1:    sha1,
					Fixable: true,
				})
			}

			if ps, err := linter.Lint(path, src); err == nil {
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
			}

			if ps, err := c.vet(vet, path, src); err == nil {
				for _, p := range ps {
					addProblem(p)
				}
			}
		}()
	}
	wg.Wait()
	sort.Sort(Problems(problems.list))
	return problems.list, nil
}

func (c *Client) vet(vet, filename string, content []byte) (Problems, error) {
	// Vet does not support reading from standard input,
	// so we write to a temporary directory and point vet at
	// a source file we write there.
	dir, err := ioutil.TempDir(c.tempDir(), "fixhub-vet")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "x.go")
	if err := ioutil.WriteFile(src, content, 0660); err != nil {
		return nil, err
	}

	cmd := exec.Command(
		vet,
		"-printfuncs=Debug:0,Debugf:0,Info:0,Infof:0,Warning:0,Warningf:0",
		src)
	// Ignore error if there's no output, because vet return status is inconsistent.
	// https://code.google.com/p/go/issues/detail?id=4980
	out, err := cmd.CombinedOutput()
	if len(out) == 0 && err != nil {
		// If there was no output then we probably couldn't execute vet at all.
		return nil, fmt.Errorf("running vet: %v", err)
	}

	var ps Problems

	scan := bufio.NewScanner(bytes.NewReader(out))
	for scan.Scan() {
		line := scan.Text()
		// line looks like
		//	x.go:301: unreachable code
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != src {
			continue
		}
		ln, err := strconv.Atoi(parts[1])
		if err != nil {
			continue // probably not a line number
		}
		text := strings.TrimSpace(parts[2])
		ps = append(ps, Problem{
			File: filename,
			Line: ln,
			Text: text,
		})
	}
	return ps, nil
}
