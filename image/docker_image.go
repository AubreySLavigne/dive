package image

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"
	"github.com/wagoodman/dive/filetree"
	"github.com/wagoodman/dive/utils"
	"golang.org/x/net/context"
)

type dockerImageAnalyzer struct {
	id        string
	Client    *client.Client
	jsonFiles map[string][]byte
	trees     []*filetree.FileTree
	layerMap  map[string]*filetree.FileTree
	layers    []*dockerLayer
}

func newDockerImageAnalyzer(imageId string) dockerImageAnalyzer {
	return dockerImageAnalyzer{
		// store discovered json files in a map so we can read the image in one pass
		jsonFiles: make(map[string][]byte),
		layerMap:  make(map[string]*filetree.FileTree),
		id:        imageId,
	}
}

func newDockerImageManifest(manifestBytes []byte) dockerImageManifest {
	var manifest []dockerImageManifest
	err := json.Unmarshal(manifestBytes, &manifest)
	if err != nil {
		logrus.Panic(err)
	}
	return manifest[0]
}

func newDockerImageConfig(configBytes []byte) dockerImageConfig {
	var imageConfig dockerImageConfig
	err := json.Unmarshal(configBytes, &imageConfig)
	if err != nil {
		logrus.Panic(err)
	}

	layerIdx := 0
	for idx := range imageConfig.History {
		if imageConfig.History[idx].EmptyLayer {
			imageConfig.History[idx].ID = "<missing>"
		} else {
			imageConfig.History[idx].ID = imageConfig.RootFs.DiffIds[layerIdx]
			layerIdx++
		}
	}

	return imageConfig
}

// Fetch creates a New API client with the local environment variables, then
// checks whether the image already exists locally. If the image does not
// exist, Fetch will attempt to pull the image from the web.
//
// Fetch will return the selected image from the docker host. It's up to the
// caller to store the images and close the stream.
func (image *dockerImageAnalyzer) Fetch() (io.ReadCloser, error) {
	var err error

	// Create a Docker client
	image.Client, err = client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}

	// Check that we have the image
	FetchImage(*image.Client, image.id)

	// Returns images from the Docker host as an io.ReadCloser stream
	return RetrieveImage(*image.Client, image.id)
}

// FetchImage verifies whether the image exists locally. If it does not,
// FetchImage will attempt to pull the image from the web
func FetchImage(c client.Client, imageID string) error {

	// An empty, non-nil context
	ctx := context.Background()

	// Check to see if the image is available locally
	_, _, err := c.ImageInspectWithRaw(ctx, imageID)
	if err != nil {
		// Image not found locally. Request the image from the web
		// Don't use the API, the CLI has more informative output
		fmt.Printf("Image not available locally. Trying to pull '%s'...\n", imageID)
		err = utils.RunDockerCmd("pull", imageID)
	}

	return err
}

// RetrieveImage returns an io.ReadCloser reference to the image with ID
// imageID. It's up to the caller to store the images and close the stream.
func RetrieveImage(c client.Client, imageID string) (io.ReadCloser, error) {

	// An empty, non-nil context
	ctx := context.Background()

	// ImageSave retrieves the image with ID/tag imageID as an io.ReadCloser
	return c.ImageSave(ctx, []string{imageID})
}

func (image *dockerImageAnalyzer) Parse(tarFile io.ReadCloser) error {
	tarReader := tar.NewReader(tarFile)

	var currentLayer uint
	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			fmt.Println(err)
			utils.Exit(1)
		}

		name := header.Name

		// some layer tars can be relative layer symlinks to other layer tars
		if header.Typeflag != tar.TypeSymlink && header.Typeflag != tar.TypeReg {
			continue
		}

		if strings.HasSuffix(name, "layer.tar") {
			currentLayer++
			if err != nil {
				return err
			}
			layerReader := tar.NewReader(tarReader)
			tree, err := processLayerTar(name, currentLayer, layerReader)
			if err != nil {
				return err
			}
			image.layerMap[name] = tree
		} else if strings.HasSuffix(name, ".json") {
			fileBuffer, err := ioutil.ReadAll(tarReader)
			if err != nil {
				return err
			}
			image.jsonFiles[name] = fileBuffer
		}
	}

	return nil
}

func (image *dockerImageAnalyzer) Analyze() (*AnalysisResult, error) {
	image.trees = make([]*filetree.FileTree, 0)

	manifest := newDockerImageManifest(image.jsonFiles["manifest.json"])
	config := newDockerImageConfig(image.jsonFiles[manifest.ConfigPath])

	// build the content tree
	for _, treeName := range manifest.LayerTarPaths {
		image.trees = append(image.trees, image.layerMap[treeName])
	}

	// build the layers array
	image.layers = make([]*dockerLayer, len(image.trees))

	// note that the image config stores images in reverse chronological order, so iterate backwards through layers
	// as you iterate chronologically through history (ignoring history items that have no layer contents)
	layerIdx := len(image.trees) - 1
	tarPathIdx := 0
	for idx := 0; idx < len(config.History); idx++ {
		// ignore empty layers, we are only observing layers with content
		if config.History[idx].EmptyLayer {
			continue
		}

		tree := image.trees[(len(image.trees)-1)-layerIdx]
		config.History[idx].Size = uint64(tree.FileSize)

		image.layers[layerIdx] = &dockerLayer{
			history: config.History[idx],
			index:   layerIdx,
			tree:    image.trees[layerIdx],
			tarPath: manifest.LayerTarPaths[tarPathIdx],
		}

		layerIdx--
		tarPathIdx++
	}

	efficiency, inefficiencies := filetree.Efficiency(image.trees)

	var sizeBytes, userSizeBytes uint64
	layers := make([]Layer, len(image.layers))
	for i, v := range image.layers {
		layers[i] = v
		sizeBytes += v.Size()
		if i != 0 {
			userSizeBytes += v.Size()
		}
	}

	var wastedBytes uint64
	for idx := 0; idx < len(inefficiencies); idx++ {
		fileData := inefficiencies[len(inefficiencies)-1-idx]
		wastedBytes += uint64(fileData.CumulativeSize)
	}

	return &AnalysisResult{
		Layers:            layers,
		RefTrees:          image.trees,
		Efficiency:        efficiency,
		UserSizeByes:      userSizeBytes,
		SizeBytes:         sizeBytes,
		WastedBytes:       wastedBytes,
		WastedUserPercent: float64(float64(wastedBytes) / float64(userSizeBytes)),
		Inefficiencies:    inefficiencies,
	}, nil
}

// processLayerTar iterates through the files in the provided tar archive and
// returns the resulting tree
func processLayerTar(name string, layerIdx uint, reader *tar.Reader) (*filetree.FileTree, error) {
	// Create a new filetree.FileTree
	tree := filetree.NewFileTree()
	tree.Name = name

	// Get a list of the files for the layer
	fileInfos, err := getFileList(reader)

	if err != nil {
		return nil, err
	}

	for _, element := range fileInfos {

		// Track the total filesize of this layer
		tree.FileSize += uint64(element.Size)

		// Add the element to the tree's list of files. This only tracks the
		// files for a single layer.
		//
		// todo: We should allow whiteout files to be not be added (thus not
		//       error out)
		_, _, err = tree.AddPath(element.Path, element)
		if err != nil {
			return nil, err
		}
	}

	return tree, nil
}

// layerParseProgress returns a string show the ongoing progress of a given layer
func layerParseProgress(layerID uint, name, progress string) string {
	return fmt.Sprintf("  ├─ [layer: %2d] %s : %s", layerID, name, progress)
}

// getFileList iterates through the provided tar.Reader and returns an array
// containing details about the included files.
func getFileList(tarReader *tar.Reader) ([]filetree.FileInfo, error) {
	var files []filetree.FileInfo

	for {
		header, err := tarReader.Next()

		// Until we have checked the full contents of the archive
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		// XGlobalHeader and XHeader are not valid headers
		if header.Typeflag == tar.TypeXGlobalHeader {
			return nil, fmt.Errorf("Provided Tar file '%s' has unexpected header '%v' (XGlobalHeader)", header.Name, header.Typeflag)
		} else if header.Typeflag == tar.TypeXHeader {
			return nil, fmt.Errorf("Provided Tar file '%s' has unexpected header '%v' (XHeader)", header.Name, header.Typeflag)
		}

		files = append(files, filetree.NewFileInfo(tarReader, header, header.Name))
	}

	return files, nil
}
