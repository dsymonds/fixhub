package main

import (
	"bytes"
	"html/template"
	"io"
	"log"
	"net/http"

	"code.google.com/p/goauth2/oauth"
	"github.com/dsymonds/fixhub"
)

type FixData struct {
	Owner    string
	Repo     string
	Problems []fixhub.Problem
}

func fix(w http.ResponseWriter, r *http.Request) {
	t := &oauth.Transport{Config: oauthConfig}
	t.Exchange(r.FormValue("code"))
	key := r.FormValue("state")

	d, err := datastore.Get(key)
	if err != nil {
		errf(w, http.StatusBadRequest, "state: %v", err)
		return
	}

	client, err := fixhub.NewClient(d.Owner, d.Repo, t.Client())
	if err != nil {
		errf(w, http.StatusInternalServerError, "%v", err)
		return
	}

	// Creating a fork on github is asynchronous, so we start with it.
	_, err = client.Branch()
	if err != nil {
		errf(w, http.StatusInternalServerError, "create fork: %v", err)
		return
	}

	var cmpURLs []string
	for _, p := range d.Problems {
		f, err := client.Fix(p)
		if err != nil {
			errf(w, http.StatusInternalServerError, "fix: %v", err)
			return
		}
		url, err := client.CommitFix(p, f)
		if err != nil {
			errf(w, http.StatusInternalServerError, "commit: %v", err)
			return
		}
		cmpURLs = append(cmpURLs, url)
	}

	datastore.Remove(key)

	if len(cmpURLs) == 1 {
		http.Redirect(w, r, cmpURLs[0], http.StatusSeeOther)
		return
	}

	buf := new(bytes.Buffer)
	if err := fixTmpl.Execute(buf, cmpURLs); err != nil {
		errf(w, http.StatusInternalServerError, "confirm: %v", err)
		log.Fatal(err)
	}
	w.Header().Set("Content-Type", "text/html")
	io.Copy(w, buf)
}

func confirm(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("state")
	_, err := datastore.Get(key) // check the key
	if err != nil {
		errf(w, http.StatusBadRequest, "%v", err)
		return
	}
	url := oauthConfig.AuthCodeURL(key)

	buf := new(bytes.Buffer)
	if err := confirmTmpl.Execute(buf, url); err != nil {
		errf(w, http.StatusInternalServerError, "confirm: %v", err)
		log.Fatal(err)
	}
	io.Copy(w, buf)
}

var fixTmpl = template.Must(template.New("fix.html").Parse(`<!DOCTYPE html>
<html>
<head>
<title>fixhub</title>
<link rel="stylesheet" type="text/css" href="/style.css">
</head>
<body class="info">
<h1>fixhub</h1>
Fixes successfully committed. To view and merge:
<ul>
{{range .}}
<li><a href="{{.}}">{{.}}</a></li>
{{end}}
</body>
</html>
`))

var confirmTmpl = template.Must(template.New("confirm.html").Parse(`<!DOCTYPE html>
<html>
<head>
<title>fixhub</title>
<link rel="stylesheet" type="text/css" href="/style.css">
<script src="/script.js" type="text/javascript"></script>
</head>
<body class="info">
<h1>fixhub</h1>
fixhub will, acting as you, fork the repository and commit changes to it.
From there, you can generate a pull request by clicking "Create Pull Request".
<p>
To do this, fixhub needs write access to your public repositories. fixhub will
only ever write to branches of repositories with a fixhub-* prefix.
<p>
<a onclick="goconfirm();" href="{{.}}">Create GitHub commit</a>
<input type="checkbox" id="donotshowagain" value="Do not show again">Do not show again
</body>
</html>
`))
