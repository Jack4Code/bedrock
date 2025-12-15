package bedrock

import (
	"io"
	"mime/multipart"
	"net/http"
)

// ... existing helpers (DecodeJSON, PathParam, QueryParam) ...

// UploadedFile represents a file from a multipart form
type UploadedFile struct {
	File     multipart.File
	Header   *multipart.FileHeader
	Filename string
	Size     int64
}

// Close closes the underlying file
func (u *UploadedFile) Close() error {
	return u.File.Close()
}

// ReadAll reads all bytes from the file
func (u *UploadedFile) ReadAll() ([]byte, error) {
	return io.ReadAll(u.File)
}

// ParseMultipartForm parses a multipart form with given max memory (in MB)
// Default is 32MB if maxMemoryMB is 0
func ParseMultipartForm(r *http.Request, maxMemoryMB int64) error {
	if maxMemoryMB == 0 {
		maxMemoryMB = 32
	}
	return r.ParseMultipartForm(maxMemoryMB << 20)
}

// GetUploadedFile gets a single file from the form
func GetUploadedFile(r *http.Request, fieldName string) (*UploadedFile, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		return nil, err
	}

	return &UploadedFile{
		File:     file,
		Header:   header,
		Filename: header.Filename,
		Size:     header.Size,
	}, nil
}

// GetUploadedFiles gets multiple files from the form (for multiple file uploads)
func GetUploadedFiles(r *http.Request, fieldName string) ([]*UploadedFile, error) {
	files := r.MultipartForm.File[fieldName]
	if len(files) == 0 {
		return nil, http.ErrMissingFile
	}

	uploaded := make([]*UploadedFile, 0, len(files))

	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, err
		}

		uploaded = append(uploaded, &UploadedFile{
			File:     file,
			Header:   header,
			Filename: header.Filename,
			Size:     header.Size,
		})
	}

	return uploaded, nil
}

// GetFormValue gets a form field value (for non-file fields in multipart form)
func GetFormValue(r *http.Request, fieldName string) string {
	return r.FormValue(fieldName)
}
