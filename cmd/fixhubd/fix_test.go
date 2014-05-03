package main

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/dsymonds/fixhub"
)

func TestFixData(t *testing.T) {
	want := FixData{
		Owner: "crawshaw",
		Repo:  "fixhub",
		Problems: []fixhub.Problem{
			{
				File:    "path/to/file1",
				Line:    42,
				Text:    "some text",
				Fixable: true,
			},
			{
				File: "path/to/file2",
				SHA1: "0beba68c67fbf55146c9f4ed4ed1f3e9617abf4d",
				Line: 7,
			},
		},
	}
	d := want.Encode()
	var got FixData
	if err := got.Decode(d); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		fmt.Printf("got.Decode(want.Encode()) = %#+v, want %#+v", got, want)
	}
}
