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

package controller

import (
	"errors"
	"time"

	"github.com/mendersoftware/deployments/resources/images"
)

// Errors expected from interface
var (
	ErrImageMetaNotFound                = errors.New("Image metadata is not found")
	ErrModelMultipartUploadMsgMalformed = errors.New("Multipart upload message malformed")
	ErrModelMissingInputMetadata        = errors.New("Missing input metadata")
	ErrModelMissingInputArtifact        = errors.New("Missing input artifact")
	ErrModelInvalidMetadata             = errors.New("Metadata invalid")
	ErrModelArtifactNotUnique           = errors.New("Artifact not unique")
	ErrModelArtifactFileTooLarge        = errors.New("Artifact file too large")
	ErrModelArtifactUploadFailed        = errors.New("Failed to upload the artifact")
	ErrModelImageInActiveDeployment     = errors.New("Image is used in active deployment and cannot be removed")
	ErrModelImageUsedInAnyDeployment    = errors.New("Image have been already used in deployment")
	ErrModelParsingArtifactFailed       = errors.New("Cannot parse artifact file")
)

type ImagesModel interface {
	ListImages(filters map[string]string) ([]*images.SoftwareImage, error)
	DownloadLink(imageID string, expire time.Duration) (*images.Link, error)
	GetImage(id string) (*images.SoftwareImage, error)
	DeleteImage(imageID string) error
	CreateImage(multipartUploadMsg *MultipartUploadMsg) (string, error)
	EditImage(id string, constructorData *images.SoftwareImageMetaConstructor) (bool, error)
}
