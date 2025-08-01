package coderd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"cdr.dev/slog"
	"github.com/coder/coder/v2/archive"
	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/database/dbtime"
	"github.com/coder/coder/v2/coderd/httpapi"
	"github.com/coder/coder/v2/coderd/httpmw"
	"github.com/coder/coder/v2/codersdk"
)

const (
	tarMimeType        = "application/x-tar"
	zipMimeType        = "application/zip"
	windowsZipMimeType = "application/x-zip-compressed"

	HTTPFileMaxBytes = 10 * (10 << 20)
)

// @Summary Upload file
// @Description Swagger notice: Swagger 2.0 doesn't support file upload with a `content-type` different than `application/x-www-form-urlencoded`.
// @ID upload-file
// @Security CoderSessionToken
// @Produce json
// @Accept application/x-tar
// @Tags Files
// @Param Content-Type header string true "Content-Type must be `application/x-tar` or `application/zip`" default(application/x-tar)
// @Param file formData file true "File to be uploaded. If using tar format, file must conform to ustar (pax may cause problems)."
// @Success 201 {object} codersdk.UploadResponse
// @Router /files [post]
func (api *API) postFile(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	apiKey := httpmw.APIKey(r)

	contentType := r.Header.Get("Content-Type")
	switch contentType {
	case tarMimeType, zipMimeType, windowsZipMimeType:
	default:
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: fmt.Sprintf("Unsupported content type header %q.", contentType),
		})
		return
	}

	r.Body = http.MaxBytesReader(rw, r.Body, HTTPFileMaxBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: "Failed to read file from request.",
			Detail:  err.Error(),
		})
		return
	}

	if contentType == zipMimeType || contentType == windowsZipMimeType {
		zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
				Message: "Incomplete .zip archive file.",
				Detail:  err.Error(),
			})
			return
		}

		data, err = archive.CreateTarFromZip(zipReader, HTTPFileMaxBytes)
		if err != nil {
			httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
				Message: "Internal error processing .zip archive.",
				Detail:  err.Error(),
			})
			return
		}
		contentType = tarMimeType
	}

	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])
	file, err := api.Database.GetFileByHashAndCreator(ctx, database.GetFileByHashAndCreatorParams{
		Hash:      hash,
		CreatedBy: apiKey.UserID,
	})
	if err == nil {
		// The file already exists!
		httpapi.Write(ctx, rw, http.StatusOK, codersdk.UploadResponse{
			ID: file.ID,
		})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error getting file.",
			Detail:  err.Error(),
		})
		return
	}

	id := uuid.New()
	file, err = api.Database.InsertFile(ctx, database.InsertFileParams{
		ID:        id,
		Hash:      hash,
		CreatedBy: apiKey.UserID,
		CreatedAt: dbtime.Now(),
		Mimetype:  contentType,
		Data:      data,
	})
	if err != nil {
		if database.IsUniqueViolation(err, database.UniqueFilesHashCreatedByKey) {
			// The file was uploaded by some concurrent process since the last time we checked for it, fetch it again.
			file, err = api.Database.GetFileByHashAndCreator(ctx, database.GetFileByHashAndCreatorParams{
				Hash:      hash,
				CreatedBy: apiKey.UserID,
			})
			api.Logger.Info(ctx, "postFile handler hit UniqueViolation trying to upload file after already checking for the file existence", slog.F("hash", hash), slog.F("created_by_id", apiKey.UserID))
		}
		// At this point the first error was either not the UniqueViolation OR there's still an error even after we
		// attempt to fetch the file again, so we should return here.
		if err != nil {
			httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
				Message: "Internal error saving file.",
				Detail:  err.Error(),
			})
			return
		}
	}

	httpapi.Write(ctx, rw, http.StatusCreated, codersdk.UploadResponse{
		ID: file.ID,
	})
}

// @Summary Get file by ID
// @ID get-file-by-id
// @Security CoderSessionToken
// @Tags Files
// @Param fileID path string true "File ID" format(uuid)
// @Success 200
// @Router /files/{fileID} [get]
func (api *API) fileByID(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx    = r.Context()
		format = r.URL.Query().Get("format")
	)

	fileID := chi.URLParam(r, "fileID")
	if fileID == "" {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: "File id must be provided in url.",
		})
		return
	}

	id, err := uuid.Parse(fileID)
	if err != nil {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: "File id must be a valid UUID.",
		})
		return
	}

	file, err := api.Database.GetFileByID(ctx, id)
	if httpapi.Is404Error(err) {
		httpapi.ResourceNotFound(rw)
		return
	}
	if err != nil {
		httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching file.",
			Detail:  err.Error(),
		})
		return
	}

	switch format {
	case codersdk.FormatZip:
		if file.Mimetype != codersdk.ContentTypeTar {
			httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
				Message: "Only .tar files can be converted to .zip format",
			})
			return
		}

		rw.Header().Set("Content-Type", codersdk.ContentTypeZip)
		rw.WriteHeader(http.StatusOK)
		err = archive.WriteZip(rw, tar.NewReader(bytes.NewReader(file.Data)), HTTPFileMaxBytes)
		if err != nil {
			api.Logger.Error(ctx, "invalid .zip archive", slog.F("file_id", fileID), slog.F("mimetype", file.Mimetype), slog.Error(err))
		}
	case "": // no format? no conversion
		rw.Header().Set("Content-Type", file.Mimetype)
		_, _ = rw.Write(file.Data)
	default:
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: "Unsupported conversion format.",
		})
	}
}
