package file

import (
	"context"
	"fmt"
	"io"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
)

type FileConfig struct {
	FileService domain.FileService
}

type FileHandler struct {
	fileService domain.FileService
}

func NewFileHandler(config *FileConfig) *FileHandler {
	return &FileHandler{
		fileService: config.FileService,
	}
}

// Handle godoc
//
//	@Summary		Upload a file
//	@Description	Upload a file to the server
//	@Description	This endpoint allows authenticated users to upload a file. The file will be processed and stored.
//	@Tags			Files
//	@Accept			multipart/form-data
//	@Produce		json
//	@Param			request	body		domain.UploadFileRequest	true	"Request body"
//	@Success		200		{object}	domain.UploadFileResponse
//	@Failure		400
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files/upload [post]
func (h *FileHandler) HandleUploadFile(ctx context.Context, req *domain.UploadFileRequest) (*domain.UploadFileResponse, int, error) {
	userId := domain.GetUserID(ctx)

	if req.FileData == nil {
		return nil, 400, fmt.Errorf("file is required")
	}

	// Open the multipart file
	file, err := req.FileData.Open()
	if err != nil {
		return nil, 500, err
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, 500, err
	}

	res, err := h.fileService.UploadFile(ctx, userId, req.FileData.Filename, fileBytes)
	if err != nil {
		return nil, 500, err
	}

	return res, 200, nil
}

// godoc
// Handle processes a GetFilesRequest and returns a GetFilesResponse.
//
//	@Summary		Retrieve files for the authenticated user
//	@Description	Retrieves a list of files associated with the authenticated user.
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	domain.GetFilesResponse
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files [get]
func (h *FileHandler) HandleGetFiles(ctx context.Context, req *domain.GetFilesRequest) (*domain.GetFilesResponse, int, error) {
	userId := domain.GetUserID(ctx)
	files, err := h.fileService.GetFilesByUserID(ctx, userId)
	if err != nil {
		return nil, 500, err
	}
	return files, 200, nil
}

// Handle godoc
//
//	@Summary		Get File
//	@Description	Retrieve a file by its ID
//	@Tags			files
//	@Accept			json
//	@Produce		application/pdf
//	@Param			id	path	string	true	"File ID"
//	@Success		200	{file}	file
//	@Failure		400
//	@Failure		404
//	@Failure		500
//	@Router			/files/{id} [get]
func (h *FileHandler) HandleGetFile(ctx context.Context, req *domain.GetFileRequest) (*domain.GetFileResponse, int, error) {
	userId := domain.GetUserID(ctx)
	file, err := h.fileService.GetFile(ctx, userId, req.Id)
	if err != nil {
		if err == domain.ErrFileNotFound {
			return nil, 404, err
		}
		if err == domain.ErrForbidden {
			return nil, 403, err
		}
		return nil, 500, err
	}
	return file, 200, nil
}

// Handle godoc
//
//	@Summary		Delete a file
//	@Description	Deletes a file and its associated data
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Param			id	path	string	true	"File ID"
//	@Success		204
//	@Failure		400
//	@Failure		401
//	@Failure		404
//	@Failure		500
//
//	@Security		BearerAuth
//
//	@Router			/files/{id} [delete]
func (h *FileHandler) HandleDeleteFile(ctx context.Context, req *domain.DeleteFileRequest) (*domain.DeleteFileResponse, int, error) {
	err := h.fileService.DeleteFile(ctx, req.Id)
	if err != nil {
		if err == domain.ErrFileNotFound {
			return nil, 404, err
		}
		return nil, 500, err
	}
	return nil, 204, nil
}

// Handle godoc
//
//	@Summary		Toggle file saved status
//	@Description	Toggle the is_saved flag of an image file. Saved images are excluded from auto-cleanup.
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Param			id	path		string	true	"File ID"
//	@Success		200	{object}	domain.ToggleFileSavedResponse
//	@Failure		400
//	@Failure		403
//	@Failure		404
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files/{id}/save [patch]
func (h *FileHandler) HandleToggleFileSaved(ctx context.Context, req *domain.ToggleFileSavedRequest) (*domain.ToggleFileSavedResponse, int, error) {
	userId := domain.GetUserID(ctx)
	res, err := h.fileService.ToggleFileSaved(ctx, userId, req.Id)
	if err != nil {
		if err == domain.ErrFileNotFound {
			return nil, 404, err
		}
		if err == domain.ErrForbidden {
			return nil, 403, err
		}
		if err == domain.ErrInvalidRequest {
			return nil, 400, err
		}
		return nil, 500, err
	}
	return res, 200, nil
}

// Handle godoc
//
//	@Summary		Submit a YouTube link
//	@Description	Submit a YouTube link to extract its transcript and make it available for chat.
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Param			request	body		domain.SubmitYouTubeRequest	true	"Request body"
//	@Success		200		{object}	domain.SubmitYouTubeResponse
//	@Failure		400
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files/youtube [post]
func (h *FileHandler) HandleSubmitYouTube(ctx context.Context, req *domain.SubmitYouTubeRequest) (*domain.SubmitYouTubeResponse, int, error) {
	userId := domain.GetUserID(ctx)

	if req.Url == "" {
		return nil, 400, fmt.Errorf("url is required")
	}

	res, err := h.fileService.SubmitYouTubeLink(ctx, userId, req.Url)
	if err != nil {
		return nil, 500, err
	}

	return res, 200, nil
}

// Handle godoc
//
//	@Summary		Extract notes from image
//	@Description	Extract text notes from an uploaded image file using OCR.
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Param			id	path		string	true	"File ID"
//	@Success		200	{object}	domain.ExtractNotesResponse
//	@Failure		400
//	@Failure		403
//	@Failure		404
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files/{id}/extract-notes [post]
func (h *FileHandler) HandleExtractNotesFromImage(ctx context.Context, req *domain.ExtractNotesRequest) (*domain.ExtractNotesResponse, int, error) {
	userId := domain.GetUserID(ctx)

	res, err := h.fileService.ExtractNotesFromImage(ctx, userId, req.Id)
	if err != nil {
		if err == domain.ErrForbidden {
			return nil, 403, err
		}
		if err == domain.ErrFileNotFound {
			return nil, 404, err
		}
		return nil, 500, err
	}

	return res, 200, nil
}

// Handle godoc
//
//	@Summary		Update extracted notes
//	@Description	Update the extracted text for a file and re-index it.
//	@Tags			Files
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"File ID"
//	@Param			request	body		domain.UpdateNotesRequest	true	"Request body"
//	@Success		200		{object}	domain.UpdateNotesResponse
//	@Failure		400
//	@Failure		403
//	@Failure		404
//	@Failure		500
//	@Security		BearerAuth
//	@Router			/files/{id}/notes [put]
func (h *FileHandler) HandleUpdateNotes(ctx context.Context, req *domain.UpdateNotesRequest) (*domain.UpdateNotesResponse, int, error) {
	userId := domain.GetUserID(ctx)

	if req.Text == "" {
		return nil, 400, fmt.Errorf("text is required")
	}

	res, err := h.fileService.UpdateNotes(ctx, userId, req.Id, req.Text)
	if err != nil {
		if err == domain.ErrForbidden {
			return nil, 403, err
		}
		if err == domain.ErrFileNotFound {
			return nil, 404, err
		}
		return nil, 500, err
	}

	return res, 200, nil
}
