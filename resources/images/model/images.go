// Copyright 2016 Mender Software AS
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//        http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package model

import (
	"io"
	"io/ioutil"
	"time"

	"github.com/mendersoftware/deployments/resources/images"
	"github.com/mendersoftware/deployments/resources/images/controller"
	"github.com/mendersoftware/mender-artifact/metadata"
	"github.com/mendersoftware/mender-artifact/parser"
	"github.com/mendersoftware/mender-artifact/reader"
	"github.com/pkg/errors"
	"github.com/satori/go.uuid"
)

const (
	ImageContentType = "application/vnd.mender-artifact"
)

type ImagesModel struct {
	fileStorage   FileStorage
	deployments   ImageUsedIn
	imagesStorage SoftwareImagesStorage
}

func NewImagesModel(
	fileStorage FileStorage,
	checker ImageUsedIn,
	imagesStorage SoftwareImagesStorage,
) *ImagesModel {
	return &ImagesModel{
		fileStorage:   fileStorage,
		deployments:   checker,
		imagesStorage: imagesStorage,
	}
}

// CreateImage parses artifact and uploads artifact file to the file storage - in parallel,
// and creates image structure in the system.
// Returns image ID and nil on success.
func (i *ImagesModel) CreateImage(
	metaConstructor *images.SoftwareImageMetaConstructor,
	imageReader io.Reader) (string, error) {
	if metaConstructor == nil {
		return "", controller.ErrModelMissingInputMetadata
	}
	if imageReader == nil {
		return "", controller.ErrModelMissingInputArtifact
	}
	artifactID, err := i.handleArtifact(metaConstructor, imageReader)
	// try to remove artifact file from file storage on error
	if err != nil {
		if cleanupErr := i.fileStorage.Delete(artifactID); cleanupErr != nil {
			return "", errors.Wrap(err, cleanupErr.Error())
		}
	}
	return artifactID, err
}

// handleArtifact parses artifact and uploads artifact file to the file storage - in parallel,
// and creates image structure in the system.
// Returns image ID, artifact file ID and nil on success.
func (i *ImagesModel) handleArtifact(
	metaConstructor *images.SoftwareImageMetaConstructor,
	imageReader io.Reader) (string, error) {

	// limit just for safety
	// max image size - 10G
	const MaxImageSize = 1024 * 1024 * 1024 * 10

	// create pipe
	pR, pW := io.Pipe()
	// limit reader to max image size
	lr := io.LimitReader(imageReader, MaxImageSize)
	tee := io.TeeReader(lr, pW)

	artifactID := uuid.NewV4().String()

	ch := make(chan error)
	// create goroutine for artifact upload
	//
	// reading from the pipe (which is done in UploadArtifact method) is a blocking operation
	// and cannot be done in the same goroutine as writing to the pipe
	//
	// uploading and parsing artifact in the same process will cause in a deadlock!
	go func() {
		err := i.fileStorage.UploadArtifact(artifactID, pR, ImageContentType)
		if err != nil {
			pR.CloseWithError(err)
		}
		ch <- err
	}()

	// parse artifact
	// artifact library reads all the data from the given reader
	metaArtifactConstructor, err := getMetaFromArchive(&tee, MaxImageSize)
	if err != nil {
		pW.Close()
		if uploadResponseErr := <-ch; uploadResponseErr != nil {
			return "", controller.ErrModelArtifactUploadFailed
		}
		return "", controller.ErrModelInvalidMetadata
	}

	// read the rest of the data,
	// just in case the artifact library did not read all the data from the reader
	_, err = io.Copy(ioutil.Discard, tee)
	if err != nil {
		pW.Close()
		_ = <-ch
		return "", err
	}

	// close the pipe
	pW.Close()

	// collect output from the goroutine
	if uploadResponseErr := <-ch; uploadResponseErr != nil {
		return "", uploadResponseErr
	}

	// validate artifact metadata
	if err = metaArtifactConstructor.Validate(); err != nil {
		return "", controller.ErrModelInvalidMetadata
	}

	// check if artifact is unique
	// artifact is considered to be unique if there is no artifact with the same name
	// and supporing the same platform in the system
	isArtifactUnique, err := i.imagesStorage.IsArtifactUnique(
		metaArtifactConstructor.ArtifactName, metaArtifactConstructor.DeviceTypesCompatible)
	if err != nil {
		return "", errors.Wrap(err, "Fail to check if artifact is unique")
	}
	if !isArtifactUnique {
		return "", controller.ErrModelArtifactNotUnique
	}

	image := images.NewSoftwareImage(artifactID, metaConstructor, metaArtifactConstructor)

	// save image structure in the system
	if err = i.imagesStorage.Insert(image); err != nil {
		return "", errors.Wrap(err, "Fail to store the metadata")
	}

	return artifactID, nil
}

// GetImage allows to fetch image obeject with specified id
// Nil if not found
func (i *ImagesModel) GetImage(id string) (*images.SoftwareImage, error) {

	image, err := i.imagesStorage.FindByID(id)
	if err != nil {
		return nil, errors.Wrap(err, "Searching for image with specified ID")
	}

	if image == nil {
		return nil, nil
	}

	return image, nil
}

// DeleteImage removes metadata and image file
// Noop for not exisitng images
// Allowed to remove image only if image is not scheduled or in progress for an updates - then image file is needed
// In case of already finished updates only image file is not needed, metadata is attached directly to device deployment
// therefore we still have some information about image that have been used (but not the file)
func (i *ImagesModel) DeleteImage(imageID string) error {
	found, err := i.GetImage(imageID)

	if err != nil {
		return errors.Wrap(err, "Getting image metadata")
	}

	if found == nil {
		return controller.ErrImageMetaNotFound
	}

	inUse, err := i.deployments.ImageUsedInActiveDeployment(imageID)
	if err != nil {
		return errors.Wrap(err, "Checking if image is used in active deployment")
	}

	// Image is in use, not allowed to delete
	if inUse {
		return controller.ErrModelImageInActiveDeployment
	}

	// Delete image file (call to external service)
	// Noop for not existing file
	if err := i.fileStorage.Delete(imageID); err != nil {
		return errors.Wrap(err, "Deleting image file")
	}

	// Delete metadata
	if err := i.imagesStorage.Delete(imageID); err != nil {
		return errors.Wrap(err, "Deleting image metadata")
	}

	return nil
}

// ListImages according to specified filers.
func (i *ImagesModel) ListImages(filters map[string]string) ([]*images.SoftwareImage, error) {

	imageList, err := i.imagesStorage.FindAll()
	if err != nil {
		return nil, errors.Wrap(err, "Searching for image metadata")
	}

	if imageList == nil {
		return make([]*images.SoftwareImage, 0), nil
	}

	return imageList, nil
}

// EditObject allows editing only if image have not been used yet in any deployment.
func (i *ImagesModel) EditImage(imageID string, constructor *images.SoftwareImageMetaConstructor) (bool, error) {

	if err := constructor.Validate(); err != nil {
		return false, errors.Wrap(err, "Validating image metadata")
	}

	found, err := i.deployments.ImageUsedInDeployment(imageID)
	if err != nil {
		return false, errors.Wrap(err, "Searching for usage of the image among deployments")
	}

	if found {
		return false, controller.ErrModelImageUsedInAnyDeployment
	}

	foundImage, err := i.imagesStorage.FindByID(imageID)
	if err != nil {
		return false, errors.Wrap(err, "Searching for image with specified ID")
	}

	if foundImage == nil {
		return false, nil
	}

	foundImage.SetModified(time.Now())

	_, err = i.imagesStorage.Update(foundImage)
	if err != nil {
		return false, errors.Wrap(err, "Updating image matadata")
	}

	return true, nil
}

// DownloadLink presigned GET link to download image file.
// Returns error if image have not been uploaded.
func (i *ImagesModel) DownloadLink(imageID string, expire time.Duration) (*images.Link, error) {

	found, err := i.imagesStorage.Exists(imageID)
	if err != nil {
		return nil, errors.Wrap(err, "Searching for image with specified ID")
	}

	if !found {
		return nil, nil
	}

	found, err = i.fileStorage.Exists(imageID)
	if err != nil {
		return nil, errors.Wrap(err, "Searching for image file")
	}

	if !found {
		return nil, nil
	}

	link, err := i.fileStorage.GetRequest(imageID, expire, ImageContentType)
	if err != nil {
		return nil, errors.Wrap(err, "Generating download link")
	}

	return link, nil
}

func getArtifactInfo(info metadata.Info) *images.ArtifactInfo {
	return &images.ArtifactInfo{
		Format:  info.Format,
		Version: uint(info.Version),
	}
}

func getUpdateFiles(maxImageSize int64, uFiles map[string]parser.UpdateFile) ([]images.UpdateFile, error) {
	var files []images.UpdateFile
	for _, u := range uFiles {
		if u.Size > maxImageSize {
			return nil, errors.New("Image too large")
		}
		files = append(files, images.UpdateFile{
			Name:      u.Name,
			Size:      u.Size,
			Signature: string(u.Signature),
			Date:      &u.Date,
			Checksum:  string(u.Checksum),
		})
	}
	return files, nil
}

func getMetaFromArchive(
	r *io.Reader, maxImageSize int64) (*images.SoftwareImageMetaArtifactConstructor, error) {
	metaArtifact := images.NewSoftwareImageMetaArtifactConstructor()

	aReader := areader.NewReader(*r)
	defer aReader.Close()

	data, err := aReader.Read()
	if err != nil {
		return nil, errors.Wrap(err, "reading artifact error")
	}
	metaArtifact.Info = getArtifactInfo(aReader.GetInfo())
	metaArtifact.DeviceTypesCompatible = aReader.GetCompatibleDevices()
	metaArtifact.ArtifactName = aReader.GetArtifactName()

	for _, p := range data {
		uFiles, err := getUpdateFiles(maxImageSize, p.GetUpdateFiles())
		if err != nil {
			return nil, errors.Wrap(err, "Cannot get update files:")
		}

		metaArtifact.Updates = append(
			metaArtifact.Updates,
			images.Update{
				TypeInfo: images.ArtifactUpdateTypeInfo{
					Type: p.GetUpdateType().Type,
				},
				MetaData: p.GetMetadata(),
				Files:    uFiles,
			})
	}

	return metaArtifact, nil
}
