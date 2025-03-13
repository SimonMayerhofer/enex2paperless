package enex

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"enex2paperless/internal/config"
	"enex2paperless/pkg/paperless"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/spf13/afero"
)

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
			// Check if the file type is in the exclude list
			if len(settings.ExcludeFileTypes) > 0 {
				// Extract the extension from the MIME type
				extension, err := getExtensionFromMimeType(mimeType)
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

// Extract the file extension from the MIME type
func getExtensionFromMimeType(mimeType string) (string, error) {
	mimeToExt := map[string]string{
		// Microsoft Office - Modern formats
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx",
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

	// If outputFolder is specified, we don't need to initialize task tracker or make API calls
	if outputFolder != "" {
		slog.Info("output folder specified, skipping API calls and task processing", "folder", outputFolder)
	} else {
		// Initialize task tracker only if linking is enabled
		if settings.LinkFieldID > 0 {
			e.taskTracker = paperless.NewTaskTracker("tasks.json")
			e.taskTracker.Fs = e.Fs
			if err := e.taskTracker.Load(); err != nil {
				slog.Error("failed to load tasks", "error", err)
				return fmt.Errorf("failed to load tasks: %v", err)
			}
		} else {
			slog.Info("skipping task tracking - no link field ID specified")
		}
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
					id, err := paperless.UploadFile(e.client, documentTitle, mdFileName, "text/markdown", []byte(mdContent), note, url, e.taskTracker, failedNoteChannel)
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

										// If we're using an output folder, write the PDF file to disk
										if outputFolder != "" {
											pdfFilePath := filepath.Join(outputFolder, uploadFileName)
											if err := afero.WriteFile(e.Fs, pdfFilePath, pdfData, 0644); err != nil {
												slog.Error("failed to write converted PDF file to disk",
													"error", err,
													"file", pdfFilePath)
											} else {
												slog.Debug("wrote converted PDF file to disk",
													"file", pdfFilePath,
													"size", len(pdfData))

												// Try to set file timestamps
												// First try to use the original file's timestamp if available
												var fileTime time.Time

												// If we have a timestamp from the original file, use it
												if !file.FileTime.IsZero() {
													fileTime = file.FileTime
													slog.Debug("using original file timestamp for PDF file",
														"file", pdfFilePath,
														"time", fileTime)
												} else if note.Created != "" {
													// Fall back to the note's creation time
													var err error
													fileTime, err = time.Parse("20060102T150405Z", note.Created)
													if err != nil {
														slog.Error("failed to parse note creation time for PDF file", "error", err)
													} else {
														slog.Debug("using note creation time for PDF file",
															"file", pdfFilePath,
															"time", fileTime)
													}
												}

												// Set the file timestamps if we have a valid time
												if !fileTime.IsZero() {
													if _, ok := e.Fs.(*afero.OsFs); ok {
														if err := os.Chtimes(pdfFilePath, fileTime, fileTime); err != nil {
															slog.Error("failed to set PDF file timestamps", "error", err)
														} else {
															slog.Debug("set PDF file timestamps", "file", pdfFilePath, "time", fileTime)
														}
													}
												} else {
													slog.Debug("could not determine timestamp for PDF file", "file", pdfFilePath)
												}
											}
										}

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

						// If we're using an output folder, we've already saved the extracted files
						// No need to upload them to Paperless
						if outputFolder != "" {
							slog.Debug("skipping upload of extracted file - using output folder",
								"file", uploadFileName,
								"output_folder", outputFolder)
							continue
						}

						id, err := paperless.UploadFile(e.client, note.Title+" | "+zipFileNameWithoutExt+" | "+fileNameWithoutExt, uploadFileName, uploadMimeType, uploadData, note, url, e.taskTracker, failedNoteChannel)
						if err != nil {
							slog.Error("failed to upload extracted file", "error", err)
						} else {
							slog.Info("uploaded note content as PDF", "document_id", id)
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

						// Get timestamp for the file - try resource timestamp first, then note creation time
						var fileTime time.Time
						if resource.ResourceAttributes.Timestamp != "" {
							parsedTime, err := time.Parse("20060102T150405Z", resource.ResourceAttributes.Timestamp)
							if err != nil {
								slog.Debug("failed to parse resource timestamp, using note creation time",
									"error", err,
									"resource_timestamp", resource.ResourceAttributes.Timestamp)
								fileTime, err = time.Parse("20060102T150405Z", note.Created)
								if err != nil {
									slog.Error("failed to parse note creation time", "error", err)
								}
							} else {
								fileTime = parsedTime
								slog.Debug("using resource timestamp for iWork file",
									"file", fileName,
									"time", fileTime)
							}
						} else {
							fileTime, err = time.Parse("20060102T150405Z", note.Created)
							if err != nil {
								slog.Error("failed to parse note creation time", "error", err)
							} else {
								slog.Debug("using note creation time for iWork file (no resource timestamp)",
									"file", fileName,
									"time", fileTime)
							}
						}

						// Set timestamp for the original iWork file before conversion
						if !fileTime.IsZero() {
							if _, ok := e.Fs.(*afero.OsFs); ok {
								if err := os.Chtimes(fileName, fileTime, fileTime); err != nil {
									slog.Error("failed to set timestamps for original iWork file", "error", err)
								} else {
									slog.Debug("set timestamps for original iWork file", "file", fileName, "time", fileTime)
								}
							}
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

							// Write the converted PDF file to disk
							if err := afero.WriteFile(e.Fs, fileName, pdfData, 0644); err != nil {
								failedNoteChannel <- note
								slog.Error(fmt.Sprintf("failed to write PDF file %v", err))
								break
							}

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

				// Check for correspondent tag prefix
				var correspondentID int
				var tagsToProcess []string
				var correspondentTagFound bool

				if settings.CorrespondentTagPrefix != "" {
					for _, tagName := range note.Tags {
						if strings.HasPrefix(tagName, settings.CorrespondentTagPrefix) {
							if !correspondentTagFound {
								// This is the first tag with the prefix, use it as correspondent
								correspondentTagFound = true

								// Remove the prefix and format the correspondent name
								correspondentName := strings.TrimPrefix(tagName, settings.CorrespondentTagPrefix)
								formattedName := paperless.FormatCorrespondentName(correspondentName)

								// Get or create correspondent
								id, err := paperless.GetCorrespondentID(formattedName)
								if err != nil {
									failedNoteChannel <- note
									slog.Error("failed to check for correspondent", "error", err)
									break
								}

								if id == 0 {
									slog.Debug("creating correspondent", "correspondent", formattedName)
									id, err = paperless.CreateCorrespondent(formattedName)
									if err != nil {
										failedNoteChannel <- note
										slog.Error("couldn't create correspondent", "error", err)
										break
									}
								} else {
									slog.Debug(fmt.Sprintf("found correspondent: %s with ID: %v", formattedName, id))
								}

								correspondentID = id
							} else {
								// This is a second or subsequent tag with the prefix, keep it as a tag
								tagsToProcess = append(tagsToProcess, tagName)
							}
						} else {
							// Regular tag without prefix
							tagsToProcess = append(tagsToProcess, tagName)
						}
					}
				} else {
					// No correspondent tag prefix set, process all tags normally
					tagsToProcess = note.Tags
				}

				// Set correspondent ID if found
				if correspondentID > 0 {
					err = writer.WriteField("correspondent", strconv.Itoa(correspondentID))
					if err != nil {
						failedNoteChannel <- note
						slog.Error("couldn't write correspondent field", "error", err)
						break
					}
				}

				// Combine processed tags and additional tags into one slice to process
				allTags := append([]string{}, tagsToProcess...)
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

				// Skip upload if we're using an output folder
				if outputFolder != "" {
					slog.Debug("skipping upload - using output folder",
						"file", uploadFileName,
						"output_folder", outputFolder)
					continue
				}

				id, err := paperless.UploadFile(e.client, documentTitle, uploadFileName, uploadMimeType, uploadData, note, url, e.taskTracker, failedNoteChannel)
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
	// Only if we're not using an output folder
	if outputFolder == "" && settings.LinkFieldID > 0 {
		slog.Info("processing all pending tasks")
		if err := paperless.ProcessPendingTasks(e.taskTracker, e.client); err != nil {
			slog.Error("failed to process pending tasks", "error", err)
		}

		// Link all documents after all tasks are processed
		slog.Info("linking all documents")
		if err := paperless.LinkDocumentsFromTasks(e.taskTracker, e.client); err != nil {
			slog.Error("failed to link documents", "error", err)
		}
	} else if outputFolder != "" {
		slog.Info("skipping task processing and document linking - using output folder")
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
	FileTime   time.Time // Original timestamp from the zip file
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

		// Get the MIME type of the file
		mimeType := getMimeType(file.Name)

		// Check if this file type should be processed
		isWantedFileType, err := checkFileType(mimeType, file.Name)
		if err != nil {
			slog.Error("error when handling MIME type", "error", err, "filename", file.Name, "mimetype", mimeType)
			continue
		}

		if !isWantedFileType {
			slog.Debug("skipping unwanted extracted file type", "filename", file.Name, "filetype", mimeType)
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
			Path:       filePath,
			Name:       file.Name,
			Data:       buf.Bytes(),
			MimeType:   mimeType,
			ZipFileName: zipFileName,
			FileTime:   fileTime,
		})

		slog.Info("extracted file from zip", "file", file.Name)
	}

	return extractedFiles, nil
}

// getMimeType returns the MIME type based on file extension
func getMimeType(filename string) string {
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
