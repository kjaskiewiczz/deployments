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
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/asaskevich/govalidator"
	"github.com/mendersoftware/deployments/resources/images"
	"github.com/mendersoftware/go-lib-micro/requestlog"
	"github.com/pkg/errors"
)

// API input validation constants
const (
	DefaultDownloadLinkExpire = 60

	// AWS limitation is 1 week
	MaxLinkExpire = 60 * 7 * 24

	DefaultMaxMetaSize = 1024 * 1024 * 10
)

var (
	ErrIDNotUUIDv4        = errors.New("ID is not UUIDv4")
	ErrInvalidExpireParam = errors.New("Invalid expire parameter")
)

type SoftwareImagesController struct {
	view  RESTView
	model ImagesModel
}

// MultipartUploadMsg is a structure with fields extracted from the mulitpart/form-data form
// send in the artifact upload request
type MultipartUploadMsg struct {
	// user metadata constructor
	MetaConstructor *images.SoftwareImageMetaConstructor
	// size of the artifact file
	ArtifactSize int64
	// reader pointing to the beginning of the artifact data
	ArtifactReader io.Reader
}

func NewSoftwareImagesController(model ImagesModel, view RESTView) *SoftwareImagesController {
	return &SoftwareImagesController{
		model: model,
		view:  view,
	}
}

func (s *SoftwareImagesController) GetImage(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	id := r.PathParam("id")

	if !govalidator.IsUUIDv4(id) {
		s.view.RenderError(w, r, ErrIDNotUUIDv4, http.StatusBadRequest, l)
		return
	}

	image, err := s.model.GetImage(id)
	if err != nil {
		s.view.RenderInternalError(w, r, err, l)
		return
	}

	if image == nil {
		s.view.RenderErrorNotFound(w, r, l)
		return
	}

	s.view.RenderSuccessGet(w, image)
}

func (s *SoftwareImagesController) ListImages(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	list, err := s.model.ListImages(r.PathParams)
	if err != nil {
		s.view.RenderInternalError(w, r, err, l)
		return
	}

	s.view.RenderSuccessGet(w, list)
}

func (s *SoftwareImagesController) DownloadLink(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	id := r.PathParam("id")

	if !govalidator.IsUUIDv4(id) {
		s.view.RenderError(w, r, ErrIDNotUUIDv4, http.StatusBadRequest, l)
		return
	}

	expire, err := s.getLinkExpireParam(r, DefaultDownloadLinkExpire)
	if err != nil {
		s.view.RenderError(w, r, err, http.StatusBadRequest, l)
		return
	}

	link, err := s.model.DownloadLink(id, expire)
	if err != nil {
		s.view.RenderInternalError(w, r, err, l)
		return
	}

	if link == nil {
		s.view.RenderErrorNotFound(w, r, l)
		return
	}

	s.view.RenderSuccessGet(w, link)
}

func (s *SoftwareImagesController) getLinkExpireParam(r *rest.Request, defaultValue uint64) (time.Duration, error) {

	expire := defaultValue
	expireStr := r.URL.Query().Get("expire")

	// Validate input
	if !govalidator.IsNull(expireStr) {
		if !s.validExpire(expireStr) {
			return 0, ErrInvalidExpireParam
		}

		var err error
		expire, err = strconv.ParseUint(expireStr, 10, 64)
		if err != nil {
			return 0, err
		}
	}

	return time.Duration(int64(expire)) * time.Minute, nil
}

func (s *SoftwareImagesController) validExpire(expire string) bool {

	if govalidator.IsNull(expire) {
		return false
	}

	number, err := strconv.ParseUint(expire, 10, 64)
	if err != nil {
		return false
	}

	if number > MaxLinkExpire {
		return false
	}

	return true
}

func (s *SoftwareImagesController) DeleteImage(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	id := r.PathParam("id")

	if !govalidator.IsUUIDv4(id) {
		s.view.RenderError(w, r, ErrIDNotUUIDv4, http.StatusBadRequest, l)
		return
	}

	if err := s.model.DeleteImage(id); err != nil {
		if err == ErrImageMetaNotFound {
			s.view.RenderErrorNotFound(w, r, l)
			return
		}
		s.view.RenderInternalError(w, r, err, l)
		return
	}

	s.view.RenderSuccessDelete(w)
}

func (s *SoftwareImagesController) EditImage(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	id := r.PathParam("id")

	if !govalidator.IsUUIDv4(id) {
		s.view.RenderError(w, r, ErrIDNotUUIDv4, http.StatusBadRequest, l)
		return
	}

	constructor, err := s.getSoftwareImageMetaConstructorFromBody(r)
	if err != nil {
		s.view.RenderError(w, r, errors.Wrap(err, "Validating request body"), http.StatusBadRequest, l)
		return
	}

	found, err := s.model.EditImage(id, constructor)
	if err != nil {
		s.view.RenderInternalError(w, r, err, l)
		return
	}

	if !found {
		s.view.RenderErrorNotFound(w, r, l)
		return
	}

	s.view.RenderSuccessPut(w)
}

func (s SoftwareImagesController) getSoftwareImageMetaConstructorFromBody(r *rest.Request) (*images.SoftwareImageMetaConstructor, error) {

	var constructor *images.SoftwareImageMetaConstructor

	if err := r.DecodeJsonPayload(&constructor); err != nil {
		return nil, err
	}

	if err := constructor.Validate(); err != nil {
		return nil, err
	}

	return constructor, nil
}

// Multipart Image/Meta upload handler.
// Request should be of type "multipart/form-data".
// First part should contain Metadata file. This file should be of type "application/json".
// Second part should contain artifact file.
func (s *SoftwareImagesController) NewImage(w rest.ResponseWriter, r *rest.Request) {
	l := requestlog.GetRequestLogger(r.Env)

	// parse content type and params according to RFC 1521
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		s.view.RenderError(w, r, err, http.StatusBadRequest, l)
		return
	}

	mr := multipart.NewReader(r.Body, params["boundary"])
	// parse multipart message
	multipartUploadMsg, err := s.parseMultipart(mr, DefaultMaxMetaSize)
	if err != nil {
		s.view.RenderError(w, r, err, http.StatusBadRequest, l)
		return
	}

	imgID, err := s.model.CreateImage(multipartUploadMsg)
	cause := errors.Cause(err)
	switch cause {
	default:
		s.view.RenderInternalError(w, r, err, l)
	case nil:
		s.view.RenderSuccessPost(w, r, imgID)
	case ErrModelArtifactNotUnique:
		l.Error(err.Error())
		s.view.RenderError(w, r, cause, http.StatusUnprocessableEntity, l)
	case ErrModelMissingInputMetadata, ErrModelMissingInputArtifact,
		ErrModelInvalidMetadata, ErrModelMultipartUploadMsgMalformed,
		ErrModelArtifactFileTooLarge, ErrModelParsingArtifactFailed:
		l.Error(err.Error())
		s.view.RenderError(w, r, cause, http.StatusBadRequest, l)
	}

	return
}

// parseMultipart parses multipart/form-data message.
func (s *SoftwareImagesController) parseMultipart(mr *multipart.Reader, maxMetaSize int64) (*MultipartUploadMsg, error) {
	multipartUploadMsg := &MultipartUploadMsg{
		MetaConstructor: &images.SoftwareImageMetaConstructor{},
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil, errors.Wrap(err, "Request does not contain artifact")
		}
		switch p.FormName() {
		case "size":
			size, err := s.getFormFieldValue(p, maxMetaSize)
			if err != nil {
				return nil, err
			}
			multipartUploadMsg.ArtifactSize, err = strconv.ParseInt(*size, 10, 64)
			if err != nil {
				return nil, err
			}
		case "description":
			desc, err := s.getFormFieldValue(p, maxMetaSize)
			if err != nil {
				return nil, err
			}
			multipartUploadMsg.MetaConstructor.Description = *desc
		case "artifact":
			// valide metadata provided by the user and the image size
			if err := multipartUploadMsg.MetaConstructor.Validate(); err != nil {
				return nil, err
			}
			// artifact size part should be provided before artifact part
			// artifact size value should be greater then 0
			if multipartUploadMsg.ArtifactSize <= 0 {
				return nil, errors.New("No size provided before the artifact part of the message or the size value is wrong.")
			}
			// HTML form can't set specific content-type, it's automatic, if not empty - it's a file
			if p.Header.Get("Content-Type") == "" {
				return nil, errors.New("The last part of the multipart/form-data message should be an artifact.")
			}
			multipartUploadMsg.ArtifactReader = p
			return multipartUploadMsg, nil
		}
	}
}

func (s *SoftwareImagesController) getFormFieldValue(p *multipart.Part, maxMetaSize int64) (*string, error) {
	metaReader := io.LimitReader(p, maxMetaSize)
	bytes, err := ioutil.ReadAll(metaReader)
	if err != nil && err != io.EOF {
		return nil, errors.Wrap(err, "Failed to obtain value for "+p.FormName())
	}

	strValue := string(bytes)
	return &strValue, nil
}
