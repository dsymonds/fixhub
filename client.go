/*
Package fixhub implements infrastructure for checking GitHub repositories
containing Go source files, and generating fixes for the problems.
*/
package fixhub

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"code.google.com/p/goauth2/oauth"
	"github.com/google/go-github/github"
)

// Client is a client for interacting with GitHub repositories.
type Client struct {
	gc          *github.Client
	owner, repo string
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
