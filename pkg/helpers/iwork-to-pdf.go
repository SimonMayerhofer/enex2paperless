package helpers

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enex2paperless/internal/config"

	"github.com/spf13/afero"
)

// Global mutex to ensure only one Apple application runs at a time
var appleAppMutex sync.Mutex

// convertAppleFileToPDF converts Apple iWork (Pages, Numbers, and Keynote) files to PDF format
// It uses AppleScript to automate the conversion process
func ConvertIWorkToPDF(fs afero.Fs, filePath string, mimeType string, noteCreatedDate string) ([]byte, string, error) {
	// Acquire the mutex to ensure only one Apple application is running at a time
	slog.Debug("acquiring lock for Apple application", "file", filePath)
	appleAppMutex.Lock()
	defer func() {
		appleAppMutex.Unlock()
		slog.Debug("released lock for Apple application", "file", filePath)
	}()

	// Check if the file exists
	exists, err := afero.Exists(fs, filePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to check if file exists: %v", err)
	}
	if !exists {
		return nil, "", fmt.Errorf("file does not exist: %s", filePath)
	}

	// Get the absolute path to the file
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get absolute path: %v", err)
	}

	// Create a temporary directory for the output PDF
	tempDir, err := os.MkdirTemp("", "apple_convert_*")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Get the filename without extension
	fileName := filepath.Base(filePath)
	fileNameWithoutExt := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	pdfFilePath := filepath.Join(tempDir, fileNameWithoutExt+".pdf")

	// Determine the app to use based on the file extension or MIME type
	var appName string

	// Check file extension first
	ext := strings.ToLower(filepath.Ext(fileName))
	mimeTypeLower := strings.ToLower(mimeType)

	if ext == ".pages" || strings.Contains(mimeTypeLower, "pages") {
		appName = "Pages"
	} else if ext == ".numbers" || strings.Contains(mimeTypeLower, "numbers") {
		appName = "Numbers"
	} else if ext == ".key" || strings.Contains(mimeTypeLower, "keynote") {
		appName = "Keynote"
	} else if mimeType == "application/zip" || mimeType == "application/octet-stream" {
		// Try to determine if this is an iWork file based on the file contents
		fileData, err := os.ReadFile(absPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read file: %v", err)
		}

		// Check if the zip file contains Apple iWork file signatures
		if bytes.Contains(fileData, []byte("Pages")) {
			appName = "Pages"
		} else if bytes.Contains(fileData, []byte("Numbers")) {
			appName = "Numbers"
		} else if bytes.Contains(fileData, []byte("Keynote")) {
			appName = "Keynote"
		} else {
			return nil, "", fmt.Errorf("could not determine Apple iWork file type for zip file: %s", filePath)
		}
	} else {
		return nil, "", fmt.Errorf("unsupported Apple iWork file type: %s", mimeType)
	}

	// Generate the AppleScript for the determined app
	script := fmt.Sprintf(`
		tell application "%s"
			set theFile to POSIX file "%s"
			set thePDF to POSIX file "%s"
			open theFile
			delay 2
			export front document to thePDF as PDF
			close front document saving no
			quit
		end tell
	`, appName, absPath, pdfFilePath)

	slog.Debug("executing AppleScript to convert file",
		"app", appName,
		"input_file", absPath,
		"output_file", pdfFilePath)

	// Execute the AppleScript
	cmd := exec.Command("osascript", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		return nil, "", fmt.Errorf("failed to execute AppleScript: %v, stderr: %s", err, stderr.String())
	}

	// Check if the PDF file was created
	pdfExists, err := afero.Exists(afero.NewOsFs(), pdfFilePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to check if PDF file exists: %v", err)
	}
	if !pdfExists {
		return nil, "", fmt.Errorf("PDF file was not created: %s", pdfFilePath)
	}

	// Read the converted PDF file
	pdfData, err := os.ReadFile(pdfFilePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read converted PDF file: %v", err)
	}

	// Try to set the creation date of the PDF file
	// First try to get the original file's creation date
	fileInfo, err := os.Stat(absPath)
	if err == nil {
		// Use the original file's modification time
		modTime := fileInfo.ModTime()
		if err := os.Chtimes(pdfFilePath, modTime, modTime); err != nil {
			slog.Debug("failed to set PDF file timestamps from original file", "error", err)
		} else {
			slog.Debug("set PDF file timestamps from original file", "time", modTime)
		}
	} else if noteCreatedDate != "" {
		// If we can't get the original file's creation date, try to use the note's creation date
		noteTime, err := time.Parse("20060102T150405Z", noteCreatedDate)
		if err == nil {
			if err := os.Chtimes(pdfFilePath, noteTime, noteTime); err != nil {
				slog.Debug("failed to set PDF file timestamps from note creation date", "error", err)
			} else {
				slog.Debug("set PDF file timestamps from note creation date", "time", noteTime)
			}
		} else {
			slog.Debug("failed to parse note creation date", "error", err, "date", noteCreatedDate)
		}
	}

	slog.Debug("successfully converted file to PDF",
		"app", appName,
		"input_file", absPath,
		"output_file", pdfFilePath,
		"pdf_size", len(pdfData))

	return pdfData, "application/pdf", nil
}

// IsIWorkFile checks if a file is an Apple iWork document
// Identifies Pages, Numbers, and Keynote files by filename extension or MIME type
func IsIWorkFile(mimeType string, filename string) bool {
	slog.Debug("checking if file is an Apple iWork document",
		"mime_type", mimeType,
		"filename", filename)

	// First check the filename if provided
	if filename != "" {
		filenameLower := strings.ToLower(filename)

		// Check for iWork file extensions
		if strings.HasSuffix(filenameLower, ".pages") ||
			strings.HasSuffix(filenameLower, ".numbers") ||
			strings.HasSuffix(filenameLower, ".key") ||
			strings.HasSuffix(filenameLower, ".pages.zip") ||
			strings.HasSuffix(filenameLower, ".numbers.zip") ||
			strings.HasSuffix(filenameLower, ".key.zip") {
			slog.Debug("file is an Apple iWork document (filename extension match)",
				"filename", filename)
			return true
		}
	}

	// Now check the MIME type if provided
	if mimeType != "" {
		mimeTypeLower := strings.ToLower(mimeType)

		// Check for exact MIME type matches
		appleFormats := []string{
			"application/vnd.apple.pages",
			"application/vnd.apple.numbers",
			"application/vnd.apple.keynote",
			"application/x-iwork-pages-sffpages",
			"application/x-iwork-numbers-sffnumbers",
			"application/x-iwork-keynote-sffkey",
		}

		for _, format := range appleFormats {
			if mimeType == format {
				slog.Debug("file is an Apple iWork document (exact MIME type match)",
					"mime_type", mimeType)
				return true
			}
		}

		// Check for substring matches in MIME type
		if strings.Contains(mimeTypeLower, "pages") ||
			strings.Contains(mimeTypeLower, "numbers") ||
			strings.Contains(mimeTypeLower, "keynote") ||
			strings.Contains(mimeTypeLower, "iwork") ||
			strings.Contains(mimeTypeLower, "apple") {
			slog.Debug("file is an Apple iWork document (MIME type substring match)",
				"mime_type", mimeType)
			return true
		}
	}

	slog.Debug("file is not an Apple iWork document",
		"mime_type", mimeType,
		"filename", filename)
	return false
}

// IsIWorkFileConvertible checks if a file is an Apple iWork document and if conversion is enabled
// It combines the IsIWorkFile check with configuration settings
func IsIWorkFileConvertible(mimeType string, filename string) bool {
	// Check if conversion is enabled in config
	settings, err := config.GetConfig()
	if err != nil {
		slog.Error("failed to get config", "error", err)
		return false
	}

	if !settings.ConvertAppleToPDF {
		slog.Debug("Apple iWork to PDF conversion is disabled in config")
		return false
	}

	// If conversion is enabled, check if this is an iWork file
	return IsIWorkFile(mimeType, filename)
}
