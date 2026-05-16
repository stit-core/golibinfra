package storagekit

import "errors"

var (
	ErrUploadFailed = errors.New("storagekit: upload failed")
	ErrNotFound     = errors.New("storagekit: object not found")
)
