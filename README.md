Fixhub
=======

Fixhub runs golint on a GitHub repository.

To install, run
```
   go get github.com/dsymonds/fixhub
```

Invoke fixhub with a GitHub repo name (e.g. `dsymonds/fixhub`).
```
   fixhub golang/lint
```

You might need a _personal access token_ to avoid getting rate limited.
Visit https://github.com/settings/applications and create one
with the `public_repo` permission. Store it in `$HOME/.fixhub-token` file.
