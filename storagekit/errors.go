package storagekit

import "errors"

// Sentinel-ошибки кита: проверяются через errors.Is, конкретика — в обёртке.
var (
	// ErrConfig — невалидный Config (формат endpoint'а, пустой бакет, part size и т.п.).
	ErrConfig = errors.New("storagekit: invalid config")
	// ErrUploadFailed — Put не сохранил объект.
	ErrUploadFailed = errors.New("storagekit: upload failed")
	// ErrSizeMismatch — тело короче или длиннее заявленного size; оборачивается вместе с ErrUploadFailed.
	ErrSizeMismatch = errors.New("storagekit: body size differs from declared size")
	// ErrGetFailed — Get не открыл объект.
	ErrGetFailed = errors.New("storagekit: get failed")
	// ErrStatFailed — Stat не получил метаданные объекта.
	ErrStatFailed = errors.New("storagekit: stat failed")
	// ErrDeleteFailed — Delete не удалил объект (пустой ключ или отказ хранилища).
	ErrDeleteFailed = errors.New("storagekit: delete failed")
	// ErrPresignFailed — presign не выдал ссылку (пустой ключ, ttl вне диапазона, отказ подписи).
	ErrPresignFailed = errors.New("storagekit: presign failed")
	// ErrNotFound — объекта с таким ключом нет; возвращается Get и Stat, но не Delete (он идемпотентен).
	ErrNotFound = errors.New("storagekit: object not found")
)
