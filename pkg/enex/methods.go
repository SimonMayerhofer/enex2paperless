package enex

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"enex2paperless/internal/config"
	"enex2paperless/pkg/helpers"
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
					mdFileName := helpers.SanitizeFilename(note.Title) + ".md"
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
					mdFileName := helpers.SanitizeFilename(note.Title) + ".md"

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
				isWantedFileType, err := helpers.CheckFileType(resource.Mime, resource.ResourceAttributes.FileName)
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

					extractedFiles, err := helpers.UnzipFile(decodedData, extractDir, e.Fs, resource.ResourceAttributes.FileName)
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
						isWantedFileType, err := helpers.CheckFileType(file.MimeType, file.Name)
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

									pdfData, pdfMimeType, err := helpers.ConvertAppleFileToPDF(e.Fs, tempFilePath, file.MimeType, note.Created)
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
						filename = helpers.SanitizeFilename(note.Title)
						slog.Info("using note title as filename", "title", note.Title, "filename", filename)
					}

					// Ensure filename has correct extension based on MIME type
					filename = helpers.EnsureCorrectExtension(filename, resource.Mime)
					slog.Debug("filename after extension check", "filename", filename, "mime_type", resource.Mime)

					// Add title prefix if enabled and titles don't match
					if settings.TitlePrefix {
						// Get the filename without extension
						ext := filepath.Ext(filename)
						filenameWithoutExt := strings.TrimSuffix(filename, ext)
						noteTitle := helpers.SanitizeFilename(note.Title)

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
					currentChecksum := helpers.CalculateChecksum(decodedData)

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

						existingChecksum := helpers.CalculateChecksum(existingData)
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
					if helpers.ShouldConvertToPDF(resource.Mime) {
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
						pdfData, pdfMimeType, err := helpers.ConvertAppleFileToPDF(e.Fs, fileName, resource.Mime, note.Created)
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

				if helpers.ShouldConvertToPDF(resource.Mime) {
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

							pdfData, pdfMimeType, err := helpers.ConvertAppleFileToPDF(e.Fs, tempFilePath, resource.Mime, note.Created)
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
