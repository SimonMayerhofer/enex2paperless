package enex

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"enex2paperless/internal/config"
	"enex2paperless/pkg/paperless"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/spf13/afero"
)

// Global mutex to synchronize Apple application usage
var appleAppMutex sync.Mutex

func (e *EnexFile) ReadFromFile(filePath string, noteChannel chan<- Note) error {
	slog.Debug(fmt.Sprintf("opening file: %v", filePath))
	file, err := e.Fs.Open(filePath)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	decoder.Strict = false

	slog.Debug("decoding XML")
	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Log this error but continue parsing
			slog.Error("XML parsing error", "error", err)
			break
		}
		switch se := t.(type) {
		case xml.StartElement:
			if se.Name.Local == "en-note" {
				continue
			}
			if se.Name.Local == "note" {
				var note Note
				err := decoder.DecodeElement(&note, &se)
				if err != nil {
					slog.Error("XML decoding error", "error", err)
					continue
				}
				noteChannel <- note
			}
		}
	}
	slog.Debug("completed XML decoding: closing noteChannel")
	close(noteChannel)
	return nil
}

func (e *EnexFile) PrintNoteInfo(noteChannel chan Note) {
	i := 0
	pdfs := 0

	for note := range noteChannel {
		i++
		var resourceInfo []string
		for _, resource := range note.Resources {
			resourceStr := resource.ResourceAttributes.FileName + " - " + resource.Mime
			resourceInfo = append(resourceInfo, resourceStr)

			if resource.Mime == "application/pdf" {
				pdfs++
			}
		}
		resourceInfoStr := strings.Join(resourceInfo, ", ")

		slog.Info(
			note.Title,
			slog.Int("Note Index", i),
			slog.String("Created At", note.Created),
			slog.String("Updated At", note.Updated),
			slog.String("Attached Files", resourceInfoStr),
			slog.String("Tags", strings.Join(note.Tags, ",")),
		)
	}
	slog.Info(fmt.Sprint("total Notes: ", i), "totalNotes", i, "pdfs", pdfs)
}

func checkFileType(mimeType string, filename string) (bool, error) {
	// Get configuration and check for errors
	settings, err := config.GetConfig()
	if err != nil {
		return false, err
	}

	// if filetypes contains "any" then allow all file types
	for _, fileType := range settings.FileTypes {
		if fileType == "any" {
			return true, nil
		}
	}

	// Check if this is an iWork file by filename
	filenameLower := strings.ToLower(filename)
	isAppleFile := strings.HasSuffix(filenameLower, ".pages") ||
	               strings.HasSuffix(filenameLower, ".numbers") ||
	               strings.HasSuffix(filenameLower, ".key") ||
	               strings.HasSuffix(filenameLower, ".pages.zip") ||
	               strings.HasSuffix(filenameLower, ".numbers.zip") ||
	               strings.HasSuffix(filenameLower, ".key.zip")

	// Also check by MIME type
	mimeTypeLower := strings.ToLower(mimeType)
	if strings.Contains(mimeTypeLower, "pages") ||
	   strings.Contains(mimeTypeLower, "numbers") ||
	   strings.Contains(mimeTypeLower, "keynote") ||
	   strings.Contains(mimeTypeLower, "iwork") {
		isAppleFile = true
	}

	// Check known Apple MIME types
	appleFileTypes := []string{
		"application/vnd.apple.pages",
		"application/vnd.apple.numbers",
		"application/vnd.apple.keynote",
		"application/x-iwork-pages-sffpages",
		"application/x-iwork-keynote-sffnumbers",
		"application/x-iwork-keynote-sffkey",
	}

	for _, appleType := range appleFileTypes {
		if mimeType == appleType {
			isAppleFile = true
			break
		}
	}

	// If this is an iWork file and ConvertAppleToPDF is not enabled, skip it
	if isAppleFile {
		slog.Debug("detected iWork file", "filename", filename, "mime_type", mimeType)
		if !settings.ConvertAppleToPDF {
			slog.Info("skipping iWork file because ConvertAppleToPDF is not enabled", "filename", filename, "mime_type", mimeType)
			return false, nil
		} else {
			slog.Debug("allowing iWork file for PDF conversion", "filename", filename, "mime_type", mimeType)
			return true, nil
		}
	}

	// For zip files, check if they might be iWork files
	if mimeType == "application/zip" || mimeType == "application/octet-stream" {
		// If the filename suggests it's an iWork file, handle it as above
		if strings.HasSuffix(filenameLower, ".pages") ||
		   strings.HasSuffix(filenameLower, ".numbers") ||
		   strings.HasSuffix(filenameLower, ".key") {
			slog.Debug("detected iWork file with zip MIME type", "filename", filename, "mime_type", mimeType)
			if !settings.ConvertAppleToPDF {
				slog.Info("skipping iWork file because ConvertAppleToPDF is not enabled", "filename", filename, "mime_type", mimeType)
				return false, nil
			} else {
				slog.Debug("allowing iWork file for PDF conversion", "filename", filename, "mime_type", mimeType)
				return true, nil
			}
		}
	}

	// Extract the extension from the MIME type
	extension, err := getExtensionFromMimeType(mimeType)
	if err != nil {
		return false, err
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

// Extract the file extension from the MIME type
func getExtensionFromMimeType(mimeType string) (string, error) {
	mimeToExt := map[string]string{
		// Microsoft Office - Modern formats
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":    "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",

		// Microsoft Office - Legacy formats
		"application/msword": "doc",
		"application/vnd.ms-excel": "xls",
		"application/vnd.ms-powerpoint": "ppt",

		// Apple iWork formats
		"application/x-iwork-keynote-sffnumbers": "numbers",
		"application/x-iwork-pages-sffpages": "pages",
		"application/x-iwork-keynote-sffkey": "key",
		"application/vnd.apple.pages": "pages",
		"application/vnd.apple.numbers": "numbers",
		"application/vnd.apple.keynote": "key",

		// Document formats
		"application/pdf": "pdf",
		"application/rtf": "rtf",

		// Image formats
		"image/jpeg": "jpg",
		"image/png": "png",
		"image/gif": "gif",
		"image/webp": "webp",
		"image/tiff": "tiff",

		// Text formats
		"text/plain": "txt",
		"text/html": "html",
		"text/csv": "csv",
		"application/xml": "xml",
		"application/json": "json",

		// Archive formats
		"application/zip": "zip",
		"application/x-rar-compressed": "rar",
		"application/x-7z-compressed": "7z",
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

// calculateChecksum calculates SHA-256 hash of data
func calculateChecksum(data []byte) string {
	hash := sha256.New()
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

// uploadFileToPaperless handles the common upload logic for both regular and extracted files
func (e *EnexFile) uploadFileToPaperless(title string, fileName string, mimeType string, data []byte, note Note, url string, failedNoteChannel chan Note) (int, error) {
	// Create a new buffer and multipart writer for form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	var bodyBytes []byte
	var docIDStr string

	// Set form fields
	err := writer.WriteField("title", title)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error setting form fields", "error", err)
		return 0, fmt.Errorf("error setting form fields: %v", err)
	}

	formattedCreatedDate, err := paperless.ConvertDateFormat(note.Created)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error converting date format", "error", err)
		return 0, fmt.Errorf("error converting date format: %v", err)
	}
	_ = writer.WriteField("created", formattedCreatedDate)

	// Get or create tag IDs
	var tagIDs []int

	// Get settings for additional tags
	settings, err := config.GetConfig()
	if err != nil {
		failedNoteChannel <- note
		slog.Error("failed to get config", "error", err)
		return 0, fmt.Errorf("failed to get config: %v", err)
	}

	// Combine note.Tags and additional tags into one slice to process
	allTags := append([]string{}, note.Tags...)
	if len(settings.AdditionalTags) > 0 {
		allTags = append(allTags, settings.AdditionalTags...)
	}

	for _, tagName := range allTags {
		id, err := paperless.GetTagID(tagName)
		if err != nil {
			failedNoteChannel <- note
			slog.Error("failed to check for tag", "error", err)
			return 0, fmt.Errorf("failed to check for tag: %v", err)
		}

		if id == 0 {
			slog.Debug("creating tag", "tag", tagName)
			id, err = paperless.CreateTag(tagName)
			if err != nil {
				failedNoteChannel <- note
				slog.Error("couldn't create tag", "error", err)
				return 0, fmt.Errorf("couldn't create tag: %v", err)
			}
		} else {
			slog.Debug(fmt.Sprintf("found tag: %s with ID: %v", tagName, id))
		}

		tagIDs = append(tagIDs, id)
	}

	// Add tag IDs to POST request
	for _, id := range tagIDs {
		err = writer.WriteField("tags", strconv.Itoa(id))
		if err != nil {
			failedNoteChannel <- note
			slog.Error("couldn't write fields", "error", err)
			return 0, fmt.Errorf("couldn't write fields: %v", err)
		}
	}

	// Create form file header
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`, fileName))
	h.Set("Content-Type", mimeType)

	// Create the file field with the header and write data into it
	part, err := writer.CreatePart(h)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error creating multipart writer", "error", err)
		return 0, fmt.Errorf("error creating multipart writer: %v", err)
	}

	_, err = io.Copy(part, bytes.NewReader(data))
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error writing file data", "error", err)
		return 0, fmt.Errorf("error writing file data: %v", err)
	}

	// Close the writer to finish the multipart content
	writer.Close()

	// Create a new HTTP request
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error creating new HTTP request", "error", err)
		return 0, fmt.Errorf("error creating new HTTP request: %v", err)
	}

	// auth
	if settings.Token != "" {
		req.Header.Set("Authorization", "Token "+settings.Token)
	} else {
		req.SetBasicAuth(settings.Username, settings.Password)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send the request
	slog.Debug("sending POST request", "file", fileName)
	slog.Debug("request details", "method", req.Method, "url", req.URL.String(), "headers", req.Header)

	resp, err := e.client.Do(req)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error making POST request", "error", err)
		return 0, fmt.Errorf("error making POST request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		failedNoteChannel <- note
		slog.Error("non 200 status code received", "status code", resp.StatusCode)
		slog.Error("response:", "body", buf.String())
		return 0, fmt.Errorf("non 200 status code received (%d): %s", resp.StatusCode, buf.String())
	}

	// Read the response body
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		failedNoteChannel <- note
		slog.Error("error reading response body", "error", err)
		return 0, fmt.Errorf("error reading response body: %v", err)
	}

	// Try to unmarshal as a string first (UUID)
	if err := json.Unmarshal(bodyBytes, &docIDStr); err == nil {
		slog.Debug("Response is a string",
			"id", docIDStr,
			"title", title,
			"filename", fileName)

		// Only create task info if linking is enabled
		if settings.LinkFieldID > 0 {
			// Create task info
			taskInfo := TaskInfo{
				TaskID:    docIDStr,
				Title:     title,
				FileName:  fileName,
				NoteTitle: note.Title,
				Status:    "PENDING",
				DateCreated: time.Now().Format(time.RFC3339),
			}

			// Add task to tracker
			if err := e.taskTracker.AddTask(taskInfo); err != nil {
				slog.Error("failed to save task info", "error", err)
				return 0, fmt.Errorf("failed to save task info: %v", err)
			}
		}

		// Return 0 as document ID since we'll get it later
		return 0, nil
	}

	// If not a string, try to unmarshal as a map
	var docDetails map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &docDetails); err != nil {
		failedNoteChannel <- note
		slog.Error("error decoding document details", "error", err)
		return 0, fmt.Errorf("error decoding document details: %v", err)
	}

	if id, ok := docDetails["id"].(float64); ok {
		slog.Debug("Found document ID in response",
			"id", id,
			"title", title)
		e.Uploads.Add(1)
		return int(id), nil
	}

	failedNoteChannel <- note
	slog.Error("no document ID found in response",
		"response", docDetails,
		"title", title)
	return 0, fmt.Errorf("no document ID found in response")
}

// convertToMarkdown converts Evernote HTML content to markdown
func convertToMarkdown(content string) string {
	// Remove XML/DTD declarations and Evernote specific tags
	xmlDeclRegex := regexp.MustCompile(`<\?xml[^>]+\?>`)
	content = xmlDeclRegex.ReplaceAllString(content, "")

	doctypeRegex := regexp.MustCompile(`<!DOCTYPE[^>]+>`)
	content = doctypeRegex.ReplaceAllString(content, "")

	// Remove en-note tags with any attributes and closing tags
	enNoteRegex := regexp.MustCompile(`<en-note[^>]*>|</en-note>`)
	content = enNoteRegex.ReplaceAllString(content, "")

	// Remove Evernote media tags (they represent attachments)
	mediaRegex := regexp.MustCompile(`<en-media[^>]+>`)
	content = mediaRegex.ReplaceAllString(content, "")

	// Create a new converter
	converter := md.NewConverter("", true, nil)

	// Add GitHub Flavored Markdown plugins except tables
	converter.Use(plugin.Strikethrough(""))
	converter.Use(plugin.TaskListItems())
	// Use table compatibility plugin for better plain text formatting
	converter.Use(plugin.TableCompat())

	// Convert HTML to Markdown
	markdown, err := converter.ConvertString(content)
	if err != nil {
		slog.Error("error converting HTML to markdown", "error", err)
		return ""
	}

	// Clean up multiple newlines and spaces
	markdown = regexp.MustCompile(`\n{3,}`).ReplaceAllString(markdown, "\n\n")
	markdown = regexp.MustCompile(`[ \t]+`).ReplaceAllString(markdown, " ")
	markdown = strings.TrimSpace(markdown)

	return markdown
}

func (e *EnexFile) UploadFromNoteChannel(noteChannel chan Note, failedNoteChannel chan Note, outputFolder string) error {
	slog.Debug("starting UploadFromNoteChannel")
	settings, err := config.GetConfig()
	if err != nil {
		slog.Error("failed to get config", "error", err)
		return fmt.Errorf("failed to get config: %v", err)
	}

	// Initialize task tracker only if linking is enabled
	if settings.LinkFieldID > 0 {
		e.taskTracker = NewTaskTracker("tasks.json")
		e.taskTracker.Fs = e.Fs
		if err := e.taskTracker.Load(); err != nil {
			slog.Error("failed to load tasks", "error", err)
			return fmt.Errorf("failed to load tasks: %v", err)
		}
	} else {
		slog.Info("skipping task tracking - no link field ID specified")
	}

	url := fmt.Sprintf("%s/api/documents/post_document/", settings.PaperlessAPI)

	for note := range noteChannel {
		// Handle markdown content if markdown conversion is enabled
		if settings.ConvertMarkdown && note.Content != "" {
			// Convert HTML content to markdown
			content := convertToMarkdown(note.Content)
			if content != "" {
				// Create markdown content with title as header
				mdContent := fmt.Sprintf("# %s\n\n", note.Title)
				mdContent += content

				if settings.OutputFolder != "" {
					// Create output directory if it doesn't exist
					if err := e.Fs.MkdirAll(settings.OutputFolder, 0755); err != nil {
						return fmt.Errorf("failed to create directory: %v", err)
					}

					// Sanitize the title for use as filename
					mdFileName := sanitizeFilename(note.Title) + ".md"
					mdFilePath := filepath.Join(settings.OutputFolder, mdFileName)

					// Write markdown file
					if err := afero.WriteFile(e.Fs, mdFilePath, []byte(mdContent), 0644); err != nil {
						return fmt.Errorf("failed to write markdown file: %v", err)
					}

					// Set file timestamps based on note creation time
					if noteTime, err := time.Parse("20060102T150405Z", note.Created); err == nil {
						if _, ok := e.Fs.(*afero.OsFs); ok {
							if err := os.Chtimes(mdFilePath, noteTime, noteTime); err != nil {
								slog.Error("failed to set markdown file timestamps", "error", err)
							} else {
								slog.Debug("set markdown file timestamps", "file", mdFilePath, "time", noteTime)
							}
						}
					} else {
						slog.Error("failed to parse note creation time for markdown file", "error", err)
					}

					slog.Info("saved note content as markdown", "path", mdFilePath)
				} else {
					// If no output folder is specified, upload the markdown content as a separate document
					documentTitle := note.Title
					mdFileName := sanitizeFilename(note.Title) + ".md"

					// Add markdown tag to the note's tags
					noteTags := append([]string{}, note.Tags...)
					noteTags = append(noteTags, "markdown")
					note.Tags = noteTags

					// Upload the markdown content as a new document
					id, err := e.uploadFileToPaperless(documentTitle, mdFileName, "text/markdown", []byte(mdContent), note, url, failedNoteChannel)
					if err != nil {
						slog.Error("failed to upload markdown content", "error", err)
					} else {
						slog.Info("uploaded note content as markdown", "document_id", id)
					}
				}
			} else {
				slog.Debug("skipping markdown file - no content after conversion", "note", note.Title)
			}
		}

		// Process attachments if any exist
		if len(note.Resources) > 0 {
			e.NumNotes.Add(1)
			var documentIDs []int
			seenIDs := make(map[int]bool)

			for _, resource := range note.Resources {
				slog.Info("processing file",
					slog.String("file", resource.ResourceAttributes.FileName),
				)

				// Check if this is an iWork file by filename
				filenameLower := strings.ToLower(resource.ResourceAttributes.FileName)
				isAppleFile := strings.HasSuffix(filenameLower, ".pages") ||
							   strings.HasSuffix(filenameLower, ".numbers") ||
							   strings.HasSuffix(filenameLower, ".key")

				// Also check by MIME type
				mimeTypeLower := strings.ToLower(resource.Mime)
				if strings.Contains(mimeTypeLower, "pages") ||
				   strings.Contains(mimeTypeLower, "numbers") ||
				   strings.Contains(mimeTypeLower, "keynote") ||
				   strings.Contains(mimeTypeLower, "iwork") {
					isAppleFile = true
				}

				// If this is an iWork file and conversion is disabled, skip it
				if isAppleFile && !settings.ConvertAppleToPDF {
					slog.Info("skipping iWork file because ConvertAppleToPDF is not enabled",
						"filename", resource.ResourceAttributes.FileName,
						"mime_type", resource.Mime)
					continue
				}

				// only process wanted file types
				isWantedFileType, err := checkFileType(resource.Mime, resource.ResourceAttributes.FileName)
				if err != nil {
					slog.Error("error when handling MIME type", "error", err)
					continue
				}

				if !isWantedFileType {
					slog.Debug("skipping unwanted file type", "filename", resource.ResourceAttributes.FileName, "filetype", resource.Mime)
					continue
				}

				// add padding if necessary
				data := resource.Data
				padding := len(data) % 4
				if padding > 0 {
					slog.Debug("adding padding", "padding", padding)
					data += strings.Repeat("=", 4-padding)
				}

				// Remove newlines and spaces from Resource.Data
				data = strings.ReplaceAll(resource.Data, "\n", "")
				data = strings.ReplaceAll(data, " ", "")

				// Validate that Resource.Data is valid base64
				validBase64 := regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)
				if !validBase64.MatchString(data) {
					slog.Error("data is not valid base64")
					continue
				}

				// Decode the base64 Resource.Data
				decodedData, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					failedNoteChannel <- note
					slog.Error("error decoding resource data", "error", err)
					break
				}

				// Handle zip files first, regardless of output folder setting
				if settings.Unzip && strings.HasSuffix(strings.ToLower(resource.ResourceAttributes.FileName), ".zip") {
					slog.Info("processing zip file", "file", resource.ResourceAttributes.FileName)

					// Create a reader from the byte slice to inspect zip contents
					zipReader, err := zip.NewReader(bytes.NewReader(decodedData), int64(len(decodedData)))
					if err != nil {
						slog.Error("failed to create zip reader", "error", err)
						continue
					}

					// Debug output for zip contents
					slog.Info("zip file contents:", "total_files", len(zipReader.File))
					for _, file := range zipReader.File {
						slog.Info("zip entry:",
							"name", file.Name,
							"size", file.UncompressedSize64,
							"compressed_size", file.CompressedSize64,
							"is_dir", file.FileInfo().IsDir())
					}

					// Create a temporary directory for extraction if output folder is not set
					extractDir := outputFolder
					if extractDir == "" {
						extractDir = os.TempDir()
					}

					extractedFiles, err := unzipFile(decodedData, extractDir, e.Fs, resource.ResourceAttributes.FileName)
					if err != nil {
						slog.Error("failed to extract zip file", "error", err)
						continue
					}

					// Track files for cleanup
					var filesToCleanup []string

					for _, file := range extractedFiles {
						slog.Info("uploading extracted file",
							"name", file.Name,
							"mime_type", file.MimeType)

						// Check if this file type should be processed
						isWantedFileType, err := checkFileType(file.MimeType, file.Name)
						if err != nil {
							slog.Error("error when handling MIME type", "error", err)
							continue
						}

						if !isWantedFileType {
							slog.Debug("skipping unwanted extracted file type", "filename", file.Name, "filetype", file.MimeType)
							continue
						}

						// Check if this is an Apple iWork file that should be converted to PDF
						uploadData := file.Data
						uploadMimeType := file.MimeType
						uploadFileName := file.Name

						// Check if this is an Apple iWork file that should be converted to PDF
						isAppleFile := false
						filenameLower := strings.ToLower(file.Name)
						mimeTypeLower := strings.ToLower(file.MimeType)

						if strings.Contains(mimeTypeLower, "apple.pages") ||
						   strings.Contains(mimeTypeLower, "apple.numbers") ||
						   strings.Contains(mimeTypeLower, "apple.keynote") ||
						   strings.Contains(mimeTypeLower, "iwork") ||
						   strings.HasSuffix(filenameLower, ".pages") ||
						   strings.HasSuffix(filenameLower, ".numbers") ||
						   strings.HasSuffix(filenameLower, ".key") {
							isAppleFile = true
						}

						if isAppleFile && settings.ConvertAppleToPDF {
							// Create a temporary file for conversion
							tempFile, err := os.CreateTemp("", "apple_file_*"+filepath.Ext(file.Name))
							if err != nil {
								slog.Error("failed to create temporary file for conversion", "error", err)
							} else {
								tempFilePath := tempFile.Name()
								_ = tempFile.Close()

								// Write the data to the temporary file
								if err := os.WriteFile(tempFilePath, file.Data, 0644); err != nil {
									slog.Error("failed to write temporary file for conversion", "error", err)
								} else {
									// Convert to PDF
									slog.Info("converting extracted iWork file to PDF before upload",
										"file", file.Name,
										"mime_type", file.MimeType)

									pdfData, pdfMimeType, err := convertAppleFileToPDF(e.Fs, tempFilePath, file.MimeType, note.Created)
									if err != nil {
										slog.Error("failed to convert extracted file to PDF",
											"error", err,
											"file", file.Name)
									} else {
										// Use the converted PDF data and MIME type
										uploadData = pdfData
										uploadMimeType = pdfMimeType
										uploadFileName = strings.TrimSuffix(file.Name, filepath.Ext(file.Name)) + ".pdf"
										slog.Info("successfully converted extracted file to PDF",
											"original_file", file.Name,
											"pdf_file", uploadFileName,
											"pdf_size", len(pdfData))
									}

									// Clean up the temporary file
									os.Remove(tempFilePath)
								}
							}
						} else if isAppleFile && !settings.ConvertAppleToPDF {
							slog.Debug("skipping Apple iWork file because ConvertAppleToPDF is not enabled",
								"filename", file.Name,
								"mime_type", file.MimeType)
							continue
						}

						fileNameWithoutExt := strings.TrimSuffix(uploadFileName, filepath.Ext(uploadFileName))
						zipFileNameWithoutExt := strings.TrimSuffix(file.ZipFileName, filepath.Ext(file.ZipFileName))
						id, err := e.uploadFileToPaperless(
							note.Title+" | "+zipFileNameWithoutExt+" | "+fileNameWithoutExt,
							uploadFileName,
							uploadMimeType,
							uploadData,
							note,
							url,
							failedNoteChannel)
						if err != nil {
							slog.Error("failed to upload extracted file", "error", err)
						}
						// Add file to cleanup list if it's in a temporary directory
						if extractDir == os.TempDir() {
							filesToCleanup = append(filesToCleanup, file.Path)
						}

						// Check for duplicate IDs at the collection point
						if !seenIDs[id] {
							seenIDs[id] = true
							documentIDs = append(documentIDs, id)
						} else {
							slog.Warn("duplicate document ID encountered during upload",
								"document_id", id,
								"file", file.Name,
								"note", note.Title)
						}
					}

					// Clean up temporary files
					if extractDir == os.TempDir() {
						for _, filePath := range filesToCleanup {
							if err := e.Fs.Remove(filePath); err != nil {
								slog.Error("failed to clean up temporary file", "file", filePath, "error", err)
							} else {
								slog.Debug("cleaned up temporary file", "file", filePath)
							}
						}
						// Try to remove the temporary directory if it's empty
						if err := e.Fs.Remove(extractDir); err != nil {
							slog.Debug("could not remove temporary directory (may not be empty)", "dir", extractDir)
						}
					}
					continue // skip to next resource
				}

				// if outputFolder is set, output to disk and continue
				if outputFolder != "" {
					if err := e.Fs.MkdirAll(outputFolder, 0755); err != nil {
						failedNoteChannel <- note
						slog.Error(fmt.Sprintf("failed to create directory: %v", err))
						break
					}

					// Use note title if filename is empty
					filename := resource.ResourceAttributes.FileName
					if filename == "" {
						filename = sanitizeFilename(note.Title)
						slog.Info("using note title as filename", "title", note.Title, "filename", filename)
					}

					// Ensure filename has correct extension based on MIME type
					filename = ensureCorrectExtension(filename, resource.Mime)
					slog.Debug("filename after extension check", "filename", filename, "mime_type", resource.Mime)

					// Add title prefix if enabled and titles don't match
					if settings.TitlePrefix {
						// Get the filename without extension
						ext := filepath.Ext(filename)
						filenameWithoutExt := strings.TrimSuffix(filename, ext)
						noteTitle := sanitizeFilename(note.Title)

						// Only add prefix if the title and filename are different
						if !strings.EqualFold(noteTitle, filenameWithoutExt) {
							filename = noteTitle + " - " + filename
							slog.Debug("added title prefix to filename", "filename", filename)
						} else {
							slog.Debug("skipping title prefix - title matches filename",
								"title", noteTitle,
								"filename", filenameWithoutExt)
						}
					}

					// Check if file exists and generate a new name with suffix if it does
					fileName := filepath.Join(outputFolder, filename)
					baseFileName := filename
					counter := 1
					foundIdentical := false

					// Calculate checksum of the current file
					currentChecksum := calculateChecksum(decodedData)

					for {
						exists, err := afero.Exists(e.Fs, fileName)
						if err != nil {
							failedNoteChannel <- note
							slog.Error(fmt.Sprintf("failed to check if file exists: %v", err))
							break
						}
						if !exists {
							break
						}

						// Read existing file and compare checksums
						existingData, err := afero.ReadFile(e.Fs, fileName)
						if err != nil {
							failedNoteChannel <- note
							slog.Error(fmt.Sprintf("failed to read existing file: %v", err))
							break
						}

						existingChecksum := calculateChecksum(existingData)
						if existingChecksum == currentChecksum {
							slog.Info("skipping identical file", "path", fileName)
							e.Uploads.Add(1)
							foundIdentical = true
							break
						}

						// Files are different, try next suffix
						ext := filepath.Ext(baseFileName)
						nameWithoutExt := strings.TrimSuffix(baseFileName, ext)
						fileName = filepath.Join(outputFolder, fmt.Sprintf("%s-%d%s", nameWithoutExt, counter, ext))
						counter++
					}

					// Skip writing if we found an identical file
					if foundIdentical {
						continue
					}

					// Write the file first
					if err := afero.WriteFile(e.Fs, fileName, decodedData, 0644); err != nil {
						failedNoteChannel <- note
						slog.Error(fmt.Sprintf("failed to write file %v", err))
						break
					}

					// Check if this is an Apple iWork file that should be converted to PDF
					if shouldConvertToPDF(resource.Mime) {
						// Write the original file first
						if err := afero.WriteFile(e.Fs, fileName, decodedData, 0644); err != nil {
							failedNoteChannel <- note
							slog.Error(fmt.Sprintf("failed to write file %v", err))
							break
						}

						// Convert to PDF
						slog.Info("converting Apple iWork file to PDF",
							"file", fileName,
							"mime_type", resource.Mime)
						pdfData, pdfMimeType, err := convertAppleFileToPDF(e.Fs, fileName, resource.Mime, note.Created)
						if err != nil {
							slog.Error("failed to convert file to PDF",
								"error", err,
								"file", fileName)
							// Continue with the original file if conversion fails
						} else {
							// Use the converted PDF data
							decodedData = pdfData
							resource.Mime = pdfMimeType

							// Update the filename to reflect the PDF extension
							ext := filepath.Ext(fileName)
							fileName = strings.TrimSuffix(fileName, ext) + ".pdf"

							slog.Info("successfully converted file to PDF",
								"original_file", filename,
								"pdf_file", filepath.Base(fileName),
								"pdf_size", len(pdfData))
						}
					} else {
						slog.Debug("file not eligible for PDF conversion",
							"file", fileName,
							"mime_type", resource.Mime,
							"extension", filepath.Ext(fileName))
					}

					// Try to get the resource's timestamp first, fall back to note's creation time
					var fileTime time.Time
					var err error

					if resource.ResourceAttributes.Timestamp != "" {
						fileTime, err = time.Parse("20060102T150405Z", resource.ResourceAttributes.Timestamp)
						if err != nil {
							slog.Debug("failed to parse resource timestamp, using note creation time",
								"error", err,
								"resource_timestamp", resource.ResourceAttributes.Timestamp)
							fileTime, err = time.Parse("20060102T150405Z", note.Created)
							if err != nil {
								slog.Error("failed to parse note creation time", "error", err)
							}
						} else {
							slog.Debug("using resource timestamp",
								"file", fileName,
								"time", fileTime)
						}
					} else {
						fileTime, err = time.Parse("20060102T150405Z", note.Created)
						if err != nil {
							slog.Error("failed to parse note creation time", "error", err)
						} else {
							slog.Debug("using note creation time (no resource timestamp)",
								"file", fileName,
								"time", fileTime)
						}
					}

					// Try to set both modification and access times if we have a valid timestamp
					if !fileTime.IsZero() {
						if _, ok := e.Fs.(*afero.OsFs); ok {
							if err := os.Chtimes(fileName, fileTime, fileTime); err != nil {
								slog.Error("failed to set file timestamps", "error", err)
							} else {
								slog.Debug("set file timestamps", "file", fileName, "time", fileTime)
							}
						}
					}

					slog.Info("saved file", "path", fileName)
					e.Uploads.Add(1)
					continue
				}

				// Create a new buffer and multipart writer for form
				body := &bytes.Buffer{}
				writer := multipart.NewWriter(body)

				// Set form fields with conditional title
				var documentTitle string
				if len(note.Resources) > 1 {
					// Get filename without extension
					filename := resource.ResourceAttributes.FileName
					extension := filepath.Ext(filename)
					filenameWithoutExt := strings.TrimSuffix(filename, extension)
					documentTitle = fmt.Sprintf("%s | %s", note.Title, filenameWithoutExt)
				} else {
					documentTitle = note.Title
				}
				err = writer.WriteField("title", documentTitle)
				if err != nil {
					failedNoteChannel <- note
					slog.Error("error setting form fields", "error", err)
					break
				}

				formattedCreatedDate, err := paperless.ConvertDateFormat(note.Created)
				if err != nil {
					failedNoteChannel <- note
					slog.Error("error converting date format", "error", err)
					break
				}
				_ = writer.WriteField("created", formattedCreatedDate)

				// Get or create tag IDs
				var tagIDs []int

				// Combine note.Tags and additional tags into one slice to process
				allTags := append([]string{}, note.Tags...)
				if len(settings.AdditionalTags) > 0 {
					allTags = append(allTags, settings.AdditionalTags...)
				}

				for _, tagName := range allTags {
					id, err := paperless.GetTagID(tagName)
					if err != nil {
						failedNoteChannel <- note
						slog.Error("failed to check for tag", "error", err)
						break
					}

					if id == 0 {
						slog.Debug("creating tag", "tag", tagName)
						id, err = paperless.CreateTag(tagName)
						if err != nil {
							failedNoteChannel <- note
							slog.Error("couldn't create tag", "error", err.Error())
							break
						}
					} else {
						slog.Debug(fmt.Sprintf("found tag: %s with ID: %v", tagName, id))
					}

					tagIDs = append(tagIDs, id)
				}

				// Add tag IDs to POST request
				for _, id := range tagIDs {
					err = writer.WriteField("tags", strconv.Itoa(id))
					if err != nil {
						failedNoteChannel <- note
						slog.Error("couldn't write fields", "error", err)
						break
					}
				}

				// Upload the file to Paperless
				if resource.ResourceAttributes.FileName == "" {
					resource.ResourceAttributes.FileName = note.Title
				}

				// Check if this is an Apple iWork file that should be converted to PDF
				uploadData := decodedData
				uploadMimeType := resource.Mime
				uploadFileName := resource.ResourceAttributes.FileName

				if shouldConvertToPDF(resource.Mime) {
					// Create a temporary file for conversion
					tempFile, err := os.CreateTemp("", "apple_file_*"+filepath.Ext(resource.ResourceAttributes.FileName))
					if err != nil {
						slog.Error("failed to create temporary file for conversion", "error", err)
					} else {
						tempFilePath := tempFile.Name()
						_ = tempFile.Close()

						// Write the data to the temporary file
						if err := os.WriteFile(tempFilePath, decodedData, 0644); err != nil {
							slog.Error("failed to write temporary file for conversion", "error", err)
						} else {
							// Convert to PDF
							slog.Info("converting iWork file to PDF before upload",
								"file", resource.ResourceAttributes.FileName,
								"mime_type", resource.Mime)

							pdfData, pdfMimeType, err := convertAppleFileToPDF(e.Fs, tempFilePath, resource.Mime, note.Created)
							if err != nil {
								slog.Error("failed to convert file to PDF",
									"error", err,
									"file", resource.ResourceAttributes.FileName)
							} else {
								// Use the converted PDF data and MIME type
								uploadData = pdfData
								uploadMimeType = pdfMimeType
								uploadFileName = strings.TrimSuffix(resource.ResourceAttributes.FileName, filepath.Ext(resource.ResourceAttributes.FileName)) + ".pdf"
								slog.Info("successfully converted file to PDF before upload",
									"original_file", resource.ResourceAttributes.FileName,
									"pdf_file", uploadFileName,
									"pdf_size", len(pdfData))
							}

							// Clean up the temporary file
							os.Remove(tempFilePath)
						}
					}
				} else {
					slog.Debug("file not eligible for PDF conversion before upload",
						"file", resource.ResourceAttributes.FileName,
						"mime_type", resource.Mime,
						"extension", filepath.Ext(resource.ResourceAttributes.FileName))
				}

				id, err := e.uploadFileToPaperless(documentTitle, uploadFileName, uploadMimeType, uploadData, note, url, failedNoteChannel)
				if err != nil {
					failedNoteChannel <- note
					slog.Error("failed to upload file", "error", err)
					break
				}
				slog.Debug("successfully uploaded file",
					"file", resource.ResourceAttributes.FileName,
					"document_id", id)

				// Check for duplicate IDs at the collection point
				if !seenIDs[id] {
					seenIDs[id] = true
					documentIDs = append(documentIDs, id)
				} else {
					slog.Warn("duplicate document ID encountered during upload",
						"document_id", id,
						"file", resource.ResourceAttributes.FileName,
						"note", note.Title)
				}
			}
		}
	}

	// Process all pending tasks after all uploads are complete
	if settings.LinkFieldID > 0 {
		slog.Info("processing all pending tasks")
		if err := e.ProcessPendingTasks(); err != nil {
			slog.Error("failed to process pending tasks", "error", err)
		}

		// Link all documents after all tasks are processed
		slog.Info("linking all documents")
		if err := e.LinkDocuments(); err != nil {
			slog.Error("failed to link documents", "error", err)
		}
	} else {
		slog.Info("skipping task processing and document linking - no link field ID specified")
	}

	return nil
}

func FailedNoteCatcher(failedNoteChannel chan Note, failedNotes *[]Note) {
	slog.Debug("starting FailedNoteCatcher")
	for note := range failedNoteChannel {
		*failedNotes = append(*failedNotes, note)
	}
}

func RetryFeeder(failedNotes *[]Note, retryChannel chan Note) {
	slog.Debug("starting RetryFeeder")
	for _, note := range *failedNotes {
		retryChannel <- note
	}
	close(retryChannel)
}

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
	Path       string
	Name       string
	Data       []byte
	MimeType   string
	ZipFileName string
}

// unzipFile takes a byte slice of a zip file and extracts its contents to the specified directory
// It returns a slice of extracted files with their paths and data
func unzipFile(data []byte, destDir string, fs afero.Fs, zipFileName string) ([]ExtractedFile, error) {
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

		// Open the file in the zip
		rc, err := file.Open()
		if err != nil {
			return extractedFiles, fmt.Errorf("failed to open file in zip: %v", err)
		}

		// Create the file path
		filePath := filepath.Join(destDir, file.Name)

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

		// Add file to extracted files list
		extractedFiles = append(extractedFiles, ExtractedFile{
			Path:       filePath,
			Name:       file.Name,
			Data:       buf.Bytes(),
			MimeType:   getMimeType(file.Name),
			ZipFileName: zipFileName,
		})

		slog.Info("extracted file from zip", "file", file.Name)
	}

	return extractedFiles, nil
}

// getMimeType returns the MIME type based on file extension
func getMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
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
	case ".pages":
		return "application/vnd.apple.pages"
	case ".numbers":
		return "application/vnd.apple.numbers"
	case ".key":
		return "application/vnd.apple.keynote"
	default:
		return "application/octet-stream"
	}
}

// LinkDocuments processes all completed tasks and links their documents
func (e *EnexFile) LinkDocuments() error {
	settings, _ := config.GetConfig()
	if settings.LinkFieldID == 0 {
		slog.Debug("skipping document linking - no link field ID specified")
		return nil
	}

	// Group tasks by note title
	tasksByNote := make(map[string][]TaskInfo)
	var remainingTasks []TaskInfo

	for _, task := range e.taskTracker.Tasks {
		// Include both successful uploads and duplicate documents
		if (task.Status == "SUCCESS" || (task.Status == "FAILURE" && task.DocumentID > 0)) && task.DocumentID > 0 {
			tasksByNote[task.NoteTitle] = append(tasksByNote[task.NoteTitle], task)
		} else {
			remainingTasks = append(remainingTasks, task)
		}
	}

	// Process each note's documents
	for noteTitle, tasks := range tasksByNote {
		if len(tasks) < 2 {
			slog.Debug("skipping document linking - less than 2 documents",
				"note", noteTitle,
				"count", len(tasks))
			continue
		}

		// Extract document IDs
		var documentIDs []int
		for _, task := range tasks {
			documentIDs = append(documentIDs, task.DocumentID)
		}

		// Link documents
		if err := e.linkDocuments(noteTitle, documentIDs); err != nil {
			slog.Error("failed to link documents",
				"error", err,
				"note", noteTitle,
				"document_ids", documentIDs)
			// If linking failed, keep these tasks
			remainingTasks = append(remainingTasks, tasks...)
		} else {
			slog.Debug("successfully linked documents",
				"note", noteTitle,
				"count", len(documentIDs),
				"document_ids", documentIDs)
		}
	}

	// Update the task tracker with remaining tasks
	e.taskTracker.Tasks = remainingTasks

	// If no tasks remain, delete the tasks.json file
	if len(remainingTasks) == 0 {
		if err := e.taskTracker.Fs.Remove(e.taskTracker.File); err != nil {
			slog.Error("failed to delete tasks file", "error", err)
		} else {
			slog.Info("deleted tasks file - all tasks completed")
		}
	} else {
		// Save remaining tasks
		if err := e.taskTracker.Save(); err != nil {
			slog.Error("failed to save remaining tasks", "error", err)
		}
	}

	return nil
}

// linkDocuments links all documents from a note together using the custom field
func (e *EnexFile) linkDocuments(noteTitle string, documentIDs []int) error {
	if len(documentIDs) < 2 {
		slog.Debug("skipping document linking - less than 2 documents",
			"count", len(documentIDs),
			"document_ids", documentIDs)
		return nil
	}

	settings, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("error getting config: %v", err)
	}

	if settings.LinkFieldID == 0 {
		slog.Debug("skipping document linking - no link field ID specified")
		return nil
	}

	// Ensure document IDs are unique to prevent self-linking issues
	uniqueIDs := make([]int, 0, len(documentIDs))
	idMap := make(map[int]bool)

	for _, id := range documentIDs {
		if !idMap[id] {
			idMap[id] = true
			uniqueIDs = append(uniqueIDs, id)
		} else {
			slog.Warn("duplicate document ID found - skipping",
				"document_id", id,
				"note", noteTitle)
		}
	}

	// If we have fewer than 2 unique IDs, skip linking
	if len(uniqueIDs) < 2 {
		slog.Warn("skipping document linking - fewer than 2 unique documents after deduplication",
			"original_count", len(documentIDs),
			"unique_count", len(uniqueIDs),
			"document_ids", documentIDs,
			"unique_ids", uniqueIDs,
			"note", noteTitle)
		return nil
	}

	slog.Debug("starting document linking with unique IDs",
		"note", noteTitle,
		"original_count", len(documentIDs),
		"unique_count", len(uniqueIDs),
		"unique_ids", uniqueIDs,
		"link_field_id", settings.LinkFieldID)

	// Use unique IDs for document linking from now on
	documentIDs = uniqueIDs

	// Update each document to link to all others
	for _, id := range documentIDs {
		url := fmt.Sprintf("%s/api/documents/%d/", settings.PaperlessAPI, id)
		slog.Debug("processing document for linking",
			"document_id", id,
			"url", url)

		// Get current document data with retries
		var docData map[string]interface{}
		maxRetries := 5
		retryDelay := 2 * time.Second
		var lastErr error

		for retry := 0; retry < maxRetries; retry++ {
			getReq, err := http.NewRequest("GET", url, nil)
			if err != nil {
				lastErr = fmt.Errorf("error creating GET request: %v", err)
				continue
			}

			if settings.Token != "" {
				getReq.Header.Set("Authorization", "Token "+settings.Token)
			} else {
				getReq.SetBasicAuth(settings.Username, settings.Password)
			}

			slog.Debug("fetching document data", "url", getReq.URL.String(), "retry", retry+1)

			getResp, err := e.client.Do(getReq)
			if err != nil {
				lastErr = fmt.Errorf("error getting document: %v", err)
				time.Sleep(retryDelay)
				continue
			}
			defer getResp.Body.Close()

			if getResp.StatusCode == 404 {
				lastErr = fmt.Errorf("document not found (404)")
				slog.Warn("document not found, skipping linking",
					"document_id", id,
					"note", noteTitle)
				// Remove this document from the list of documents to link
				documentIDs = removeInt(documentIDs, id)
				break
			}

			if getResp.StatusCode != 200 {
				var bodyBytes []byte
				bodyBytes, _ = io.ReadAll(getResp.Body)
				lastErr = fmt.Errorf("error getting document: status code %d", getResp.StatusCode)
				slog.Error("error getting document",
					"status_code", getResp.StatusCode,
					"response_body", string(bodyBytes),
					"document_id", id)
				time.Sleep(retryDelay)
				continue
			}

			var bodyBytes []byte
			bodyBytes, err = io.ReadAll(getResp.Body)
			if err != nil {
				lastErr = fmt.Errorf("error reading response body: %v", err)
				time.Sleep(retryDelay)
				continue
			}

			if err := json.Unmarshal(bodyBytes, &docData); err != nil {
				lastErr = fmt.Errorf("error decoding document data: %v", err)
				time.Sleep(retryDelay)
				continue
			}

			// If we get here, we successfully got the document data
			break
		}

		if docData == nil {
			slog.Error("failed to get document data after retries",
				"document_id", id,
				"error", lastErr)
			return fmt.Errorf("failed to get document data after %d retries: %v", maxRetries, lastErr)
		}

		// Create array of linked document IDs (excluding current document)
		var linkedIDs []int
		for _, linkedID := range documentIDs {
			if linkedID != id {
				linkedIDs = append(linkedIDs, linkedID)
			}
		}

		slog.Debug("preparing to update document",
			"document_id", id,
			"linked_ids", linkedIDs,
			"total_documents", len(documentIDs))

		// Initialize custom_fields if it doesn't exist
		if _, ok := docData["custom_fields"]; !ok {
			slog.Debug("initializing custom_fields array", "document_id", id)
			docData["custom_fields"] = []interface{}{}
		}

		// Update custom field with links
		customFields, ok := docData["custom_fields"].([]interface{})
		if !ok {
			slog.Error("custom_fields is not an array",
				"type", fmt.Sprintf("%T", docData["custom_fields"]),
				"value", docData["custom_fields"],
				"document_id", id)
			return fmt.Errorf("custom_fields is not an array")
		}

		slog.Debug("current custom fields",
			"document_id", id,
			"custom_fields", customFields)

		// Check if the link field already exists
		fieldExists := false
		for _, field := range customFields {
			if fieldMap, ok := field.(map[string]interface{}); ok {
				slog.Debug("checking custom field",
					"document_id", id,
					"field", fieldMap)
				if fieldID, ok := fieldMap["field"].(float64); ok {
					if int(fieldID) == settings.LinkFieldID {
						fieldMap["value"] = linkedIDs
						fieldExists = true
						slog.Debug("updated existing link field",
							"document_id", id,
							"field_id", settings.LinkFieldID,
							"linked_ids", linkedIDs,
							"updated_field", fieldMap)
						break
					}
				}
			}
		}

		// If the field doesn't exist, add it
		if !fieldExists {
			newField := map[string]interface{}{
				"field": float64(settings.LinkFieldID),
				"value": linkedIDs,
			}
			customFields = append(customFields, newField)
			docData["custom_fields"] = customFields
			slog.Debug("added new link field",
				"document_id", id,
				"field_id", settings.LinkFieldID,
				"linked_ids", linkedIDs,
				"new_field", newField)
		}

		// Send update request
		jsonData, err := json.Marshal(docData)
		if err != nil {
			return fmt.Errorf("error marshaling document data: %v", err)
		}

		patchReq, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf("error creating PATCH request: %v", err)
		}

		if settings.Token != "" {
			patchReq.Header.Set("Authorization", "Token "+settings.Token)
		} else {
			patchReq.SetBasicAuth(settings.Username, settings.Password)
		}
		patchReq.Header.Set("Content-Type", "application/json")

		patchResp, err := e.client.Do(patchReq)
		if err != nil {
			return fmt.Errorf("error updating document: %v", err)
		}
		defer patchResp.Body.Close()

		if patchResp.StatusCode != 200 {
			var bodyBytes []byte
			bodyBytes, _ = io.ReadAll(patchResp.Body)
			slog.Error("error updating document",
				"status_code", patchResp.StatusCode,
				"response_body", string(bodyBytes),
				"request_data", string(jsonData),
				"document_id", id)
			return fmt.Errorf("error updating document: status code %d", patchResp.StatusCode)
		}

		slog.Debug("successfully updated document", "document_id", id)
	}

	slog.Info("successfully linked all documents",
		"note", noteTitle,
		"document_count", len(documentIDs),
		"document_ids", documentIDs,
		"link_field_id", settings.LinkFieldID)
	return nil
}

// Helper function to remove an integer from a slice
func removeInt(slice []int, s int) []int {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// Helper function to get map keys for logging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ProcessPendingTasks checks the status of pending tasks and updates their status
func (e *EnexFile) ProcessPendingTasks() error {
	settings, _ := config.GetConfig()
	pendingTasks := e.taskTracker.GetPendingTasks()

	// Keep checking until all tasks are done
	for len(pendingTasks) > 0 {
		slog.Info("checking pending tasks", "count", len(pendingTasks))

		for _, task := range pendingTasks {
			url := fmt.Sprintf("%s/api/tasks/?task_id=%s", settings.PaperlessAPI, task.TaskID)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				slog.Error("error creating GET request", "error", err)
				continue
			}

			if settings.Token != "" {
				req.Header.Set("Authorization", "Token "+settings.Token)
			} else {
				req.SetBasicAuth(settings.Username, settings.Password)
			}

			resp, err := e.client.Do(req)
			if err != nil {
				slog.Error("error getting task details", "error", err)
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				bodyBytes, _ := io.ReadAll(resp.Body)
				slog.Error("error getting task details",
					"status code", resp.StatusCode,
					"response body", string(bodyBytes))
				continue
			}

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				slog.Error("error reading response body", "error", err)
				continue
			}

			var taskList []map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &taskList); err != nil {
				slog.Error("error decoding task list", "error", err)
				continue
			}

			if len(taskList) > 0 {
				taskData := taskList[0]
				status, _ := taskData["status"].(string)
				dateDone, _ := taskData["date_done"].(string)
				result, _ := taskData["result"].(string)

				// Update task info
				task.Status = status
				task.DateDone = dateDone

				// Handle duplicate document case
				if status == "FAILURE" && strings.Contains(result, "duplicate") {
					if relatedDoc, ok := taskData["related_document"].(string); ok {
						if docID, err := strconv.Atoi(relatedDoc); err == nil {
							task.DocumentID = docID
							task.Status = "SUCCESS" // Mark as success since we found the duplicate
							slog.Info("found duplicate document",
								"task_id", task.TaskID,
								"original_doc_id", docID,
								"note", task.NoteTitle)
						}
					}
				} else if status == "SUCCESS" {
					if relatedDoc, ok := taskData["related_document"].(string); ok {
						if docID, err := strconv.Atoi(relatedDoc); err == nil {
							task.DocumentID = docID
						}
					}
				}

				if err := e.taskTracker.UpdateTask(task.TaskID, task); err != nil {
					slog.Error("failed to update task", "error", err)
				}
			}
		}

		// Save tasks to file after each batch of updates
		if err := e.taskTracker.Save(); err != nil {
			slog.Error("failed to save tasks", "error", err)
		}

		// Get updated list of pending tasks
		pendingTasks = e.taskTracker.GetPendingTasks()

		// If there are still pending tasks, wait before checking again
		if len(pendingTasks) > 0 {
			slog.Info("waiting for tasks to complete", "pending_count", len(pendingTasks))
			time.Sleep(2 * time.Second) // Wait 2 seconds before checking again
		}
	}

	slog.Info("all tasks completed")
	return nil
}

// sanitizeFilename removes or replaces characters that are invalid in filenames
func sanitizeFilename(filename string) string {
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

// ensureCorrectExtension makes sure the filename has the correct extension based on MIME type
func ensureCorrectExtension(filename string, mimeType string) string {
	if mimeType == "" {
		// Even if no MIME type, ensure existing extension is lowercase
		ext := filepath.Ext(filename)
		if ext != "" {
			basename := strings.TrimSuffix(filename, ext)
			return basename + strings.ToLower(ext)
		}
		return filename
	}

	expectedExt, err := getExtensionFromMimeType(mimeType)
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

// convertAppleFileToPDF converts Apple iWork (Pages, Numbers, and Keynote) files to PDF format
// It uses AppleScript to automate the conversion process
func convertAppleFileToPDF(fs afero.Fs, filePath string, mimeType string, noteCreatedDate string) ([]byte, string, error) {
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

// shouldConvertToPDF checks if the file should be converted to PDF
// Currently supports Apple iWork (Pages, Numbers, and Keynote) files
func shouldConvertToPDF(mimeType string) bool {
	// Get settings
	settings, err := config.GetConfig()
	if err != nil {
		slog.Error("failed to get config", "error", err)
		return false
	}

	// Check if conversion is enabled
	if !settings.ConvertAppleToPDF {
		slog.Debug("Apple iWork to PDF conversion is disabled in config")
		return false
	}

	slog.Debug("checking if file should be converted to PDF", "mime_type", mimeType)

	// Check if the MIME type is one of the known Apple formats
	mimeTypeLower := strings.ToLower(mimeType)

	// Check for exact MIME type matches
	appleFormats := []string{
		"application/vnd.apple.pages",
		"application/vnd.apple.numbers",
		"application/vnd.apple.keynote",
		"application/x-iwork-pages-sffpages",
		"application/x-iwork-keynote-sffnumbers",
		"application/x-iwork-keynote-sffkey",
	}

	for _, format := range appleFormats {
		if mimeType == format {
			slog.Debug("file will be converted to PDF (exact MIME type match)", "mime_type", mimeType)
			return true
		}
	}

	// Check for substring matches
	if strings.Contains(mimeTypeLower, "pages") ||
	   strings.Contains(mimeTypeLower, "numbers") ||
	   strings.Contains(mimeTypeLower, "keynote") ||
	   strings.Contains(mimeTypeLower, "iwork") {
		slog.Debug("file will be converted to PDF (MIME type substring match)", "mime_type", mimeType)
		return true
	}

	// Special case for zip files that might be Apple iWork files
	// This should be handled by the caller checking the filename

	slog.Debug("file will not be converted to PDF", "mime_type", mimeType)
	return false
}
