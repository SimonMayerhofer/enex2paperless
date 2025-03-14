package helpers

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"enex2paperless/internal/config"

	"github.com/spf13/afero"
)

// isSystemFile checks if a file or directory is a system file that should be excluded
func isSystemFile(name string) bool {
	// Convert to lowercase for case-insensitive comparison
	name = strings.ToLower(name)

	// Check for common system files and directories
	systemFiles := []string{
		".ds_store",
		"thumbs.db",
		"desktop.ini",
		"__macosx",
		"._",
	}

	for _, systemFile := range systemFiles {
		if strings.Contains(name, systemFile) {
			return true
		}
	}

	return false
}

// ExtractedFile represents a file extracted from a zip archive
type ExtractedFile struct {
	Path        string
	Name        string
	Data        []byte
	MimeType    string
	ZipFileName string
	FileTime    time.Time // Original timestamp from the zip file
}

// UnzipFile takes a byte slice of a zip file and extracts its contents to the specified directory
// It returns a slice of extracted files with their paths and data
func UnzipFile(data []byte, destDir string, fs afero.Fs, zipFileName string, noteTitle string) ([]ExtractedFile, error) {
	var extractedFiles []ExtractedFile

	// Create a reader from the byte slice
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to create zip reader: %v", err)
	}

	// Create destination directory if it doesn't exist
	if err := fs.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %v", err)
	}

	// Extract each file
	for _, file := range zipReader.File {
		// Skip directories and system files
		if file.FileInfo().IsDir() || isSystemFile(file.Name) {
			slog.Debug("skipping system file or directory", "file", file.Name)
			continue
		}

		// Get the MIME type of the file
		mimeType := GetMimeType(file.Name)

		// Check if this file type should be processed
		isAllowed, err := IsAllowedFileType(mimeType, file.Name)
		if err != nil {
			slog.Error("error when handling MIME type", "error", err, "filename", file.Name, "mimetype", mimeType)
			continue
		}

		if !isAllowed {
			slog.Debug("skipping unwanted extracted file type", "filename", file.Name, "filetype", mimeType)
			continue
		}

		// Open the file in the zip
		rc, err := file.Open()
		if err != nil {
			return extractedFiles, fmt.Errorf("failed to open file in zip: %v", err)
		}

		uploadFileName := file.Name

		settings, err := config.GetConfig()
		if err != nil {
			slog.Error("failed to get config", "error", err)
			return extractedFiles, fmt.Errorf("failed to get config: %v", err)
		}
		// Apply title prefix if enabled & output folder is specified
		if settings.TitlePrefix && settings.OutputFolder != "" {
			// Get the filename without extension
			ext := filepath.Ext(uploadFileName)
			filenameWithoutExt := strings.TrimSuffix(uploadFileName, ext)
			noteTitle = SanitizeFilename(noteTitle)

			// Only add prefix if the title and filename are different
			if !strings.EqualFold(noteTitle, filenameWithoutExt) {
				uploadFileName = noteTitle + " - " + uploadFileName
				slog.Debug("added title prefix to extracted file", "filename", uploadFileName)
			} else {
				slog.Debug("skipping title prefix - title matches extracted filename",
					"title", noteTitle,
					"filename", filenameWithoutExt)
			}
		}

		// Create the file path
		filePath := filepath.Join(destDir, uploadFileName)

		// Read the file contents
		var buf bytes.Buffer
		_, err = io.Copy(&buf, rc)
		rc.Close()
		if err != nil {
			return extractedFiles, fmt.Errorf("failed to read file contents: %v", err)
		}

		// Create the file
		f, err := fs.Create(filePath)
		if err != nil {
			return extractedFiles, fmt.Errorf("failed to create file: %v", err)
		}

		// Write the contents
		_, err = f.Write(buf.Bytes())
		f.Close()
		if err != nil {
			return extractedFiles, fmt.Errorf("failed to write file contents: %v", err)
		}

		// Get the original timestamp from the zip file
		fileTime := file.Modified
		if fileTime.IsZero() {
			// Fall back to the legacy MS-DOS timestamp if the extended timestamp is not available
			fileTime = file.FileInfo().ModTime()
		}

		// Set the file's timestamp to match the original file in the zip
		if !fileTime.IsZero() {
			// Only attempt to set timestamps if we're using the OS filesystem
			if _, ok := fs.(*afero.OsFs); ok {
				if err := os.Chtimes(filePath, fileTime, fileTime); err != nil {
					slog.Error("failed to set file timestamps for extracted file",
						"error", err,
						"file", file.Name,
						"time", fileTime)
				} else {
					slog.Debug("set file timestamps for extracted file",
						"file", file.Name,
						"time", fileTime)
				}
			}
		} else {
			slog.Debug("could not determine original timestamp for extracted file", "file", file.Name)
		}

		// Add file to extracted files list
		extractedFiles = append(extractedFiles, ExtractedFile{
			Path:        filePath,
			Name:        uploadFileName,
			Data:        buf.Bytes(),
			MimeType:    mimeType,
			ZipFileName: zipFileName,
			FileTime:    fileTime,
		})

		slog.Info("extracted file from zip", "file", file.Name)
	}

	return extractedFiles, nil
}

// getMimeType returns the MIME type based on file extension
func GetMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	// Document formats
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".rtf":
		return "application/rtf"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".md", ".markdown":
		return "text/markdown"

	// Image formats
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".bmp":
		return "image/bmp"

	// Microsoft Office formats
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"

	// Apple iWork formats
	case ".pages":
		return "application/vnd.apple.pages"
	case ".numbers":
		return "application/vnd.apple.numbers"
	case ".key":
		return "application/vnd.apple.keynote"

	// Archive formats
	case ".zip":
		return "application/zip"
	case ".rar":
		return "application/x-rar-compressed"
	case ".7z":
		return "application/x-7z-compressed"
	case ".tar":
		return "application/x-tar"
	case ".gz", ".gzip":
		return "application/gzip"

	// Executable formats
	case ".exe":
		return "application/x-msdownload"
	case ".bat":
		return "application/x-bat"
	case ".sh":
		return "application/x-sh"

	// Default for unknown types
	default:
		slog.Debug("unknown file extension, using default MIME type", "filename", filename, "extension", ext)
		return "application/octet-stream"
	}
}

// CalculateChecksum calculates SHA-256 hash of data
func CalculateChecksum(data []byte) string {
	hash := sha256.New()
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

// Extract the file extension from the MIME type
func GetExtensionFromMimeType(mimeType string) (string, error) {
	mimeToExt := map[string]string{
		// Microsoft Office - Modern formats
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",

		// Microsoft Office - Legacy formats
		"application/msword":            "doc",
		"application/vnd.ms-excel":      "xls",
		"application/vnd.ms-powerpoint": "ppt",

		// Apple iWork formats
		"application/x-iwork-keynote-sffnumbers": "numbers",
		"application/x-iwork-pages-sffpages":     "pages",
		"application/x-iwork-keynote-sffkey":     "key",
		"application/vnd.apple.pages":            "pages",
		"application/vnd.apple.numbers":          "numbers",
		"application/vnd.apple.keynote":          "key",

		// Document formats
		"application/pdf": "pdf",
		"application/rtf": "rtf",

		// Image formats
		"image/jpeg": "jpg",
		"image/png":  "png",
		"image/gif":  "gif",
		"image/webp": "webp",
		"image/tiff": "tiff",

		// Text formats
		"text/plain":       "txt",
		"text/html":        "html",
		"text/csv":         "csv",
		"application/xml":  "xml",
		"application/json": "json",

		// Archive formats
		"application/zip":              "zip",
		"application/x-rar-compressed": "rar",
		"application/x-7z-compressed":  "7z",
	}

	// Check if we have a direct mapping
	if ext, ok := mimeToExt[mimeType]; ok {
		return ext, nil
	}

	// If no direct mapping, split and use the subtype
	parts := strings.Split(mimeType, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MIME type format: %s", mimeType)
	}

	// For unknown types, return empty string to indicate no known mapping
	return "", nil
}

func IsAllowedFileType(mimeType string, filename string) (bool, error) {
	// Get configuration and check for errors
	settings, err := config.GetConfig()
	if err != nil {
		return false, err
	}

	// if filetypes contains "any" then allow all file types
	for _, fileType := range settings.FileTypes {
		if fileType == "any" {
			// Check if the file type is in the exclude list
			if len(settings.ExcludeFileTypes) > 0 {
				// Extract the extension from the MIME type
				extension, err := GetExtensionFromMimeType(mimeType)
				if err != nil {
					return false, err
				}

				// If no extension was found from MIME type, try to get it from the filename
				if extension == "" {
					fileExt := filepath.Ext(filename)
					if fileExt != "" {
						// Remove the leading dot
						extension = fileExt[1:]
					}
				}

				// Convert extension and excluded file types to lowercase for case-insensitive comparison
				extensionLower := strings.ToLower(extension)
				excludedFileTypes := make([]string, len(settings.ExcludeFileTypes))
				for i, fileType := range settings.ExcludeFileTypes {
					excludedFileTypes[i] = strings.ToLower(fileType)
				}

				// Check if the extension matches any excluded file type
				for _, excludedType := range excludedFileTypes {
					if extensionLower == excludedType {
						slog.Debug("skipping excluded file type", "filename", filename, "filetype", mimeType, "extension", extensionLower)
						return false, nil
					}
				}
			}
			return true, nil
		}
	}

	// Extract the extension from the MIME type
	extension, err := GetExtensionFromMimeType(mimeType)
	if err != nil {
		return false, err
	}

	// If no extension was found from MIME type, try to get it from the filename
	if extension == "" {
		fileExt := filepath.Ext(filename)
		if fileExt != "" {
			// Remove the leading dot
			extension = fileExt[1:]
		}
	}

	// Convert extension and allowed file types to lowercase for case-insensitive comparison
	extensionLower := strings.ToLower(extension)
	allowedFileTypes := make([]string, len(settings.FileTypes))
	for i, fileType := range settings.FileTypes {
		allowedFileTypes[i] = strings.ToLower(fileType)
		if fileType == "txt" {
			allowedFileTypes[i] = "plain"
		}
	}

	// Check if the extension matches any allowed file type
	for _, allowedType := range allowedFileTypes {
		if extensionLower == allowedType {
			return true, nil
		}
	}

	return false, nil
}

// SanitizeFilename removes or replaces characters that are invalid in filenames
func SanitizeFilename(filename string) string {
	// Replace invalid characters with underscores
	invalidChars := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)
	filename = invalidChars.ReplaceAllString(filename, "_")

	// Remove leading/trailing spaces and dots
	filename = strings.Trim(filename, " .")

	// If filename is empty after sanitization, return a default name
	if filename == "" {
		return "untitled"
	}

	return filename
}

// EnsureCorrectExtension makes sure the filename has the correct extension based on MIME type
func EnsureCorrectExtension(filename string, mimeType string) string {
	if mimeType == "" {
		// Even if no MIME type, ensure existing extension is lowercase
		ext := filepath.Ext(filename)
		if ext != "" {
			basename := strings.TrimSuffix(filename, ext)
			return basename + strings.ToLower(ext)
		}
		return filename
	}

	expectedExt, err := GetExtensionFromMimeType(mimeType)
	if err != nil || expectedExt == "" {
		// If error getting MIME extension or no known mapping,
		// preserve the existing extension but ensure it's lowercase
		ext := filepath.Ext(filename)
		if ext != "" {
			basename := strings.TrimSuffix(filename, ext)
			return basename + strings.ToLower(ext)
		}
		return filename
	}

	// Normalize extensions
	expectedExt = strings.ToLower(expectedExt)
	switch expectedExt {
	case "jpeg":
		expectedExt = "jpg"
	case "plain":
		expectedExt = "txt"
	}

	// Get current extension
	currentExt := strings.ToLower(filepath.Ext(filename))
	if currentExt == "" {
		// No extension, add the expected one
		return filename + "." + expectedExt
	}

	// Remove the dot from current extension
	currentExt = strings.TrimPrefix(currentExt, ".")

	// If extensions don't match and we have a known mapping, replace with correct one
	if currentExt != expectedExt {
		// Remove current extension and add the correct one
		basename := strings.TrimSuffix(filename, filepath.Ext(filename))
		return basename + "." + expectedExt
	}

	// Extensions match or we're keeping the existing one, but ensure it's lowercase
	basename := strings.TrimSuffix(filename, filepath.Ext(filename))
	return basename + "." + currentExt
}

// SetFileTimestamps sets the access and modification times for a file
// Works with afero filesystem abstraction and handles logging
func SetFileTimestamps(fs afero.Fs, filePath string, fileTime time.Time, fileDesc string) bool {
	if fileTime.IsZero() {
		slog.Debug("skipping timestamp setting - no valid time provided", "file", filePath)
		return false
	}

	// Only attempt to set timestamps if we're using the OS filesystem
	if _, ok := fs.(*afero.OsFs); ok {
		if err := os.Chtimes(filePath, fileTime, fileTime); err != nil {
			slog.Error(fmt.Sprintf("failed to set %s timestamps", fileDesc),
				"error", err,
				"file", filePath)
			return false
		} else {
			slog.Debug(fmt.Sprintf("set %s timestamps", fileDesc),
				"file", filePath,
				"time", fileTime)
			return true
		}
	} else {
		slog.Debug("skipping timestamp setting - not using OS filesystem", "file", filePath)
	}

	return false
}
