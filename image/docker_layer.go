package image

import (
	"fmt"
	"strings"

	humanize "github.com/dustin/go-humanize"
	"github.com/wagoodman/dive/filetree"
)

const (
	LayerFormat = "%-25s %7s  %s"
)

// Layer represents a Docker image layer and metadata
type dockerLayer struct {
	tarPath string
	history dockerImageHistoryEntry
	index   int
	tree    *filetree.FileTree
}

// ShortId returns the truncated id of the current layer.
func (layer *dockerLayer) TarId() string {
	return strings.TrimSuffix(layer.tarPath, "/layer.tar")
}

// ShortId returns the truncated id of the current layer.
func (layer *dockerLayer) Id() string {
	return layer.history.ID
}

// index returns the relative position of the layer within the image.
func (layer *dockerLayer) Index() int {
	return layer.index
}

// Size returns the number of bytes that this image is.
func (layer *dockerLayer) Size() uint64 {
	return layer.history.Size
}

// Tree returns the file tree representing the current layer.
func (layer *dockerLayer) Tree() *filetree.FileTree {
	return layer.tree
}

// ShortId returns the truncated id of the current layer.
func (layer *dockerLayer) Command() string {
	return strings.TrimPrefix(layer.history.CreatedBy, "/bin/sh -c ")
}

// ShortId returns the truncated id of the current layer.
func (layer *dockerLayer) ShortId() string {
	rangeBound := 25
	if len(layer.history.ID) > rangeBound {
		return layer.history.ID[0:25]
	}
	return layer.history.ID
}

// String represents a layer in a columnar format.
func (layer *dockerLayer) String() string {

	var src string
	if layer.index == 0 {
		src = "FROM " + layer.ShortId()
	} else {
		src = layer.Command()
	}

	return fmt.Sprintf(LayerFormat, layer.ShortId(), humanize.Bytes(layer.Size()), src)
}
