package storagekit

import "errors"

var (
	ErrUploadFailed  = errors.New("storagekit: upload failed")
	ErrDeleteFailed  = errors.New("storagekit: delete failed")
	ErrPresignFailed = errors.New("storagekit: presign failed")
)
