package main

import (
	"github.com/corbym/gocrest/is"
	"github.com/corbym/gocrest/then"
	"testing"
)

func Test_getallfiles(t *testing.T) {
	files := getAllFiles("testdata")
	then.AssertThat(t, len(files), is.EqualTo(4))
}

func Test_getDuplicatePath(t *testing.T) {
	file := imgInfo{path: "testdata/subfolder/IMG_6652.jpg"}
	dup := getDuplicatePath(file)
	then.AssertThat(t, dup.path, is.EqualTo("testdata/duplicates/subfolder/IMG_6652.jpg"))
}

func Test_getOriginalPath(t *testing.T) {
	file := imgInfo{path: "testdata/duplicates/subfolder/IMG_6652.jpg"}
	dup := getOriginalPath(file)
	then.AssertThat(t, dup.path, is.EqualTo("testdata/subfolder/IMG_6652.jpg"))
}
