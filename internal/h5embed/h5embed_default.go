//go:build !android

package h5embed

import "net/http"

// FS 嵌入的前端文件系统（Android APK 构建时可用），非 Android 构建返回 nil。
var FS http.FileSystem = nil
