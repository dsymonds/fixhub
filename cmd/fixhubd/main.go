// The binary fixhubd is a server to fix GitHub repositories.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsymonds/fixhub"
)

var (
	accessTokenFile = flag.String("access_token_file", filepath.Join(os.Getenv("HOME"), ".fixhub-token"), "a file containing a GitHub access token")
	rev             = flag.String("rev", "master", "revision of the repo to check")
	httpAddr        = flag.String("http", ":6061", "HTTP service address")
)

var (
	accessToken = ""
	start       = time.Now()
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: fixhubd [options]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	flag.Parse()

	if pat, err := ioutil.ReadFile(*accessTokenFile); err == nil {
		// security check
		fi, err := os.Stat(*accessTokenFile)
		if err != nil {
			log.Fatalf("os.Stat(%q): %v", *accessTokenFile, err)
		}
		if fi.Mode()&0077 != 0 { // check that no group/world perm bits are set
			log.Fatalf("%s is too accessible; run `chmod go= %s` to fix", *accessTokenFile, *accessTokenFile)
		}
		accessToken = string(bytes.TrimSpace(pat))
	}

	mainTextBuf := new(bytes.Buffer)
	if err := problemsTmpl.Execute(mainTextBuf, Data{}); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/github.com/", fixhubHandler)
	staticHandler("/style.css", styleText)
	staticHandler("/script.js", scriptText)
	staticHandler("/", mainTextBuf.String())
	log.Fatal(http.ListenAndServe(*httpAddr, nil))
}

func staticHandler(name, text string) {
	b := []byte(text)
	http.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, name, start, bytes.NewReader(b))
	})
}

type Data struct {
	Path     string
	Rev      string
	Owner    string
	Repo     string
	Problems fixhub.Problems
}

func fixhubHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[1:]
	parts := strings.Split(path[len("github.com/"):], "/")
	if len(parts) != 2 {
		errf(w, http.StatusBadRequest, "not a valid github owner/repo: %v", parts)
		return
	}
	owner, repo := parts[0], parts[1]

	client, err := fixhub.NewClient(owner, repo, accessToken)
	if err != nil {
		errf(w, http.StatusBadRequest, "%v", err)
		return
	}

	ps, err := client.Check(*rev)
	if err != nil {
		errf(w, http.StatusInternalServerError, "checking: %v", err)
		return
	}

	data := Data{
		Path:     path,
		Rev:      *rev,
		Owner:    owner,
		Repo:     repo,
		Problems: ps,
	}

	buf := new(bytes.Buffer)
	if err := problemsTmpl.Execute(buf, data); err != nil {
		errf(w, http.StatusInternalServerError, "%v", err)
		return
	}
	io.Copy(w, buf)
}

func errf(w http.ResponseWriter, code int, format string, a ...interface{}) {
	buf := new(bytes.Buffer)
	err := errorTmpl.Execute(buf, struct{ Code, Text string }{
		Code: strconv.Itoa(code),
		Text: fmt.Sprintf(format, a...),
	})
	if err != nil {
		log.Printf("error rendering error: %v (%s/%d)", err, format, code)
		return
	}
	w.WriteHeader(code)
	io.Copy(w, buf)
}

var errorTmpl = template.Must(template.New("error.html").Parse(`<!DOCTYPE html>
<html>
<head>
<title>golint error {{.Code}}</title>
</head>
<body>
{{.Text}}
</body>
</html>
`))

const styleText = `
body {
	font-family: Helvetica, Arial;
}
#header #repoText {
	width: 350px;
}
#header {
	font-size: 18pt;
	margin: 0 auto;
	width: 700px;
}
#header input {
	font-family: Helvetica, Arial;
	font-size: 18pt;
}
`

const scriptText = `
function goproblems() {
	var path = document.forms[0].repoText.value;
	window.location = window.location.origin + "/" + path;
	return false;
}
`

func problemLink(d Data, p fixhub.Problem) string {
	url := "https://" + d.Path + "/blob/" + d.Rev + "/" + p.File
	if p.Line > 0 {
		url += fmt.Sprintf("#L%d", p.Line)
	}
	return url
}

var problemsTmpl = template.Must(template.New("problems.html").Funcs(template.FuncMap{
	"problemLink": problemLink,
}).Parse(`<!DOCTYPE html>
<html>
<head>
<title>fixhub</title>
<link rel="stylesheet" type="text/css" href="/style.css">
<script src="/script.js" type="text/javascript"></script>
</head>
<body>

<div id="header">
<form onsubmit="return goproblems();">
Find problems in <input id="repoText" placeholder="github.com/owner/repo" value="{{.Path}}">
<input type="submit" value="Go">
</form>
</div>

{{if .Problems}}
<ul>
{{range .Problems}}
<li><a href="{{problemLink $ .}}">{{.File}}{{with .Line}}:{{.}}{{end}}</a>: {{.Text}}</li>
{{end}}
</ul>
{{end}}
</body>
</html>
`))
