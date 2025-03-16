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
	"net/http"
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

// parseTimestamp parses Evernote timestamp format (20060102T150405Z)
// If primaryTimestamp fails, it falls back to fallbackTimestamp
// Returns the parsed time and a boolean indicating success
func parseTimestamp(primaryTimestamp string, fallbackTimestamp string) (time.Time, bool) {
	var fileTime time.Time
	var err error

	if primaryTimestamp != "" {
		fileTime, err = time.Parse("20060102T150405Z", primaryTimestamp)
		if err != nil {
			slog.Debug("failed to parse primary timestamp, trying fallback",
				"error", err,
				"primary_timestamp", primaryTimestamp)

			if fallbackTimestamp != "" {
				fileTime, err = time.Parse("20060102T150405Z", fallbackTimestamp)
				if err != nil {
					slog.Error("failed to parse fallback timestamp",
						"error", err,
						"fallback_timestamp", fallbackTimestamp)
					return time.Time{}, false
				} else {
					slog.Debug("using fallback timestamp",
						"fallback_timestamp", fallbackTimestamp,
						"time", fileTime)
					return fileTime, true
				}
			} else {
				return time.Time{}, false
			}
		} else {
			slog.Debug("using primary timestamp",
				"primary_timestamp", primaryTimestamp,
				"time", fileTime)
			return fileTime, true
		}
	} else if fallbackTimestamp != "" {
		fileTime, err = time.Parse("20060102T150405Z", fallbackTimestamp)
		if err != nil {
			slog.Error("failed to parse fallback timestamp",
				"error", err,
				"fallback_timestamp", fallbackTimestamp)
			return time.Time{}, false
		} else {
			slog.Debug("using fallback timestamp (no primary timestamp)",
				"fallback_timestamp", fallbackTimestamp,
				"time", fileTime)
			return fileTime, true
		}
	}

	return time.Time{}, false
}

// convertIWorkFileToPDF checks if a file is an Apple iWork file and converts it to PDF if needed
// Parameters:
// - filePath: The full path to the file on disk that needs to be converted
// - baseFileName: The original filename (without path) used for logging purposes
// - mimeType: The MIME type of the file
// - noteCreated: The creation timestamp of the note in Evernote format (20060102T150405Z)
// - resourceTimestamp: The timestamp of the resource in Evernote format (can be empty)
// - outputFolder: Optional output folder; if provided and source is in a temp dir, the PDF will be copied there
//
// Returns:
// - string: The MIME type, which will be "application/pdf" if conversion succeeded
// - string: The new file path with .pdf extension if conversion succeeded
// - bool: True if conversion was successful, false otherwise
// - error: Any error that occurred during conversion
func (e *EnexFile) convertIWorkFileToPDF(filePath string, baseFileName string, mimeType string, noteCreated string, resourceTimestamp string, outputFolder string) (string, string, bool, error) {
	// Check if this is an Apple iWork file that should be converted to PDF
	if !helpers.IsIWorkFileConvertible(mimeType, baseFileName) {
		slog.Debug("file not eligible for PDF conversion",
			"file", filePath,
			"mime_type", mimeType,
			"extension", filepath.Ext(filePath))
		// Return original file info
		return mimeType, filePath, false, nil
	}

	// Get timestamp for the file using the helper function
	fileTime, _ := parseTimestamp(resourceTimestamp, noteCreated)

	// Set timestamp for the original iWork file before conversion
	helpers.SetFileTimestamps(e.Fs, filePath, fileTime, "original iWork file")

	// Convert to PDF
	slog.Info("converting Apple iWork file to PDF",
		"file", filePath,
		"mime_type", mimeType)
	pdfData, pdfMimeType, err := helpers.ConvertIWorkToPDF(e.Fs, filePath, mimeType, noteCreated)
	if err != nil {
		slog.Error("failed to convert file to PDF",
			"error", err,
			"file", filePath)
		// Return original file info
		return mimeType, filePath, false, err
	}

	// Update the filename to reflect the PDF extension
	ext := filepath.Ext(filePath)
	newFilePath := strings.TrimSuffix(filePath, ext) + ".pdf"

	// Write the PDF file directly to disk
	if err := afero.WriteFile(e.Fs, newFilePath, pdfData, 0644); err != nil {
		slog.Error("failed to write PDF file",
			"error", err,
			"file", newFilePath)
		return mimeType, filePath, false, err
	}

	// Set timestamp for the PDF file
	helpers.SetFileTimestamps(e.Fs, newFilePath, fileTime, "PDF file")

	// If this is a temp file and we have an output folder, copy to the output folder
	if outputFolder != "" && strings.Contains(newFilePath, os.TempDir()) {
		pdfOutputName := filepath.Base(newFilePath)
		pdfOutputPath := filepath.Join(outputFolder, pdfOutputName)

		// Check for file existence and handle duplicates
		finalOutputPath := pdfOutputPath
		counter := 1

		for {
			exists, _ := afero.Exists(e.Fs, finalOutputPath)
			if !exists {
				break
			}

			// Files are different, try next suffix
			ext := filepath.Ext(pdfOutputName)
			nameWithoutExt := strings.TrimSuffix(pdfOutputName, ext)
			finalOutputPath = filepath.Join(outputFolder, fmt.Sprintf("%s-%d%s", nameWithoutExt, counter, ext))
			counter++
		}

		// Write the PDF to the output folder
		if err := afero.WriteFile(e.Fs, finalOutputPath, pdfData, 0644); err != nil {
			slog.Error("failed to save PDF to output folder",
				"error", err,
				"output_path", finalOutputPath)
		} else {
			// Set the same timestamp on the output file
			helpers.SetFileTimestamps(e.Fs, finalOutputPath, fileTime, "output PDF file")

			slog.Info("saved converted PDF file to output folder",
				"original_file", baseFileName,
				"pdf_file", filepath.Base(finalOutputPath),
				"output_path", finalOutputPath)

			e.Uploads.Add(1)

			// Update the return path to the output file
			newFilePath = finalOutputPath
		}
	}

	slog.Info("successfully converted file to PDF",
		"original_file", baseFileName,
		"pdf_file", filepath.Base(newFilePath),
		"pdf_size", len(pdfData))

	return pdfMimeType, newFilePath, true, nil
}

// decodeBase64ResourceData handles the decoding of base64-encoded resource data
// including padding, cleaning, validation, and decoding
func (e *EnexFile) decodeBase64ResourceData(rawData string) ([]byte, error) {
	// Clean the data first (remove newlines and spaces)
	data := strings.ReplaceAll(rawData, "\n", "")
	data = strings.ReplaceAll(data, " ", "")

	// Add padding if necessary
	remainder := len(data) % 4
	if remainder > 0 {
		if remainder == 1 {
			// Length mod 4 = 1 is not valid for base64
			return nil, fmt.Errorf("invalid base64 data length")
		}
		// Add the correct number of padding characters
		paddingNeeded := 4 - remainder
		slog.Debug("adding padding", "padding_needed", paddingNeeded)
		data += strings.Repeat("=", paddingNeeded)
	}

	// Validate that data is valid base64
	validBase64 := regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)
	if !validBase64.MatchString(data) {
		slog.Error("data is not valid base64", "data", data)
		return nil, fmt.Errorf("data is not valid base64")
	}

	// Decode the base64 data
	decodedData, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		slog.Error("error decoding resource data", "error", err)
		return nil, fmt.Errorf("error decoding resource data: %v", err)
	}

	return decodedData, nil
}

// SaveResourceAsFile saves a resource (attachment) from a note to the filesystem
// Returns the path to the saved file and any error that occurred
func (e *EnexFile) SaveResourceAsFile(outputFolder string, note Note, resource Resource, decodedData []byte) (string, error) {
	// Create output directory if it doesn't exist
	if err := e.Fs.MkdirAll(outputFolder, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %v", err)
	}

	// Get config settings
	settings, err := config.GetConfig()
	if err != nil {
		return "", fmt.Errorf("failed to get config: %v", err)
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
			return "", fmt.Errorf("failed to check if file exists: %v", err)
		}
		if !exists {
			break
		}

		// Read existing file and compare checksums
		existingData, err := afero.ReadFile(e.Fs, fileName)
		if err != nil {
			return "", fmt.Errorf("failed to read existing file: %v", err)
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
		return fileName, nil
	}

	// Write the file
	if err := afero.WriteFile(e.Fs, fileName, decodedData, 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	// Check for iWork file and convert to PDF if needed. Saves to disk as well.
	pdfMimeType, pdfFilePath, converted, err := e.convertIWorkFileToPDF(
		fileName,                              // Full path to the file on disk
		filename,                              // Original base filename for logging
		resource.Mime,                         // MIME type of the file
		note.Created,                          // Creation timestamp of the note
		resource.ResourceAttributes.Timestamp, // Resource timestamp if available
		outputFolder,                          // Pass the output folder
	)

	if err == nil && converted {
		// Update variables for further processing
		resource.Mime = pdfMimeType
		fileName = pdfFilePath
	}

	// Try to get the resource's timestamp using the helper function
	fileTime, hasTime := parseTimestamp(resource.ResourceAttributes.Timestamp, note.Created)

	// Try to set both modification and access times if we have a valid timestamp
	if hasTime {
		helpers.SetFileTimestamps(e.Fs, fileName, fileTime, "file")
	}

	slog.Debug("saved file", "path", fileName)
	e.Uploads.Add(1)
	return fileName, nil
}

// convertIWorkDataToPDF handles all aspects of PDF conversion for files:
// - Detects if a file is an iWork file that should be converted
// - Checks if conversion is enabled in settings
// - Creates a temporary file with exact original filename
// - Converts the file to PDF if appropriate
// - Returns the converted or original file data as needed
//
// This centralizes all conversion-related logic in one place
func (e *EnexFile) convertIWorkDataToPDF(
	fileData []byte, // Raw file data
	originalFilename string, // Original filename (for preserving exact name)
	mimeType string, // MIME type of the file
	noteCreated string, // Creation timestamp of the note in Evernote format
	resourceTimestamp string, // Timestamp of the resource in Evernote format (can be empty)
	fileTime time.Time, // Parsed file time (can be zero)
	outputFolder string, // Optional output folder
) ([]byte, string, string, bool, error) {
	// Check if this file should be converted to PDF using the centralized helper function
	if !helpers.IsIWorkFileConvertible(mimeType, originalFilename) {
		// File is not eligible for conversion or conversion is disabled
		return fileData, mimeType, originalFilename, false, nil
	}

	// Log that we're going to convert this file
	slog.Debug("Apple iWork file detected, converting to PDF",
		"filename", originalFilename,
		"filetype", mimeType)

	// Create a temporary directory to hold our file with exact name
	tempDir, err := os.MkdirTemp("", "enex_temp_*")
	if err != nil {
		slog.Error("failed to create temporary directory for conversion", "error", err)
		return fileData, mimeType, originalFilename, false, err
	}

	// Use exact filename in the temporary directory
	exactFilename := filepath.Base(originalFilename)
	tempFilePath := filepath.Join(tempDir, exactFilename)

	// Write the data to the temporary file with exact name
	if err := os.WriteFile(tempFilePath, fileData, 0644); err != nil {
		os.RemoveAll(tempDir) // Clean up on error
		slog.Error("failed to write temporary file for conversion", "error", err)
		return fileData, mimeType, originalFilename, false, err
	}

	// Set file timestamp if available
	if !fileTime.IsZero() {
		helpers.SetFileTimestamps(e.Fs, tempFilePath, fileTime, "temporary file")
	}

	// Format timestamp for conversion function if needed
	formattedTimestamp := resourceTimestamp
	if resourceTimestamp == "" && !fileTime.IsZero() {
		formattedTimestamp = fileTime.Format("20060102T150405Z")
	}

	// Convert to PDF using our existing function
	pdfMimeType, pdfFilePath, converted, err := e.convertIWorkFileToPDF(
		tempFilePath,       // Full path to the temporary file
		originalFilename,   // Original filename for logging
		mimeType,           // MIME type
		noteCreated,        // Creation timestamp of the note
		formattedTimestamp, // Resource timestamp if available
		outputFolder,       // Optional output folder
	)

	// Default result values
	resultData := fileData
	resultMimeType := mimeType
	resultFilename := originalFilename

	if err != nil {
		slog.Error("failed to convert file to PDF",
			"error", err,
			"file", originalFilename)
	} else if converted {
		// Read the PDF file for upload if conversion was successful
		pdfData, readErr := afero.ReadFile(e.Fs, pdfFilePath)
		if readErr != nil {
			slog.Error("failed to read converted PDF file",
				"error", readErr,
				"file", pdfFilePath)
		} else {
			// Update return values with PDF data
			resultData = pdfData
			resultMimeType = pdfMimeType
			resultFilename = filepath.Base(pdfFilePath)

			slog.Info("successfully converted file to PDF",
				"original_file", originalFilename,
				"pdf_file", resultFilename,
				"pdf_size", len(pdfData))
		}
	}

	// Clean up the temporary directory and its contents
	os.RemoveAll(tempDir)

	return resultData, resultMimeType, resultFilename, converted, err
}

// UploadFromNoteChannel processes notes from a channel and uploads them to Paperless-NGX or saves to a folder
func (e *EnexFile) UploadFromNoteChannel(noteChannel <-chan Note, failedNoteChannel chan<- Note, outputFolder string, totalNotes int) error {
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
		tracker, err := paperless.InitTaskTracker(paperless.TaskTrackerOptions{
			File:           "tasks.json",
			Fs:             e.Fs,
			CheckLinkField: true,
		})

		if err != nil {
			slog.Error("failed to initialize task tracker", "error", err)
			return fmt.Errorf("failed to initialize task tracker: %v", err)
		}

		e.taskTracker = tracker
	}

	url := fmt.Sprintf("%s/api/documents/post_document/", settings.PaperlessAPI)

	for note := range noteChannel {
		// Increment the atomic counters and get the new values
		noteNumber := e.CurrentNote.Add(1)
		noteNumberAll := e.CurrentNoteAll.Add(1)
		totalNotesAll := e.TotalNotesAll.Load()

		// Log with both individual file progress and overall progress
		if totalNotesAll > 0 {
			slog.Info(fmt.Sprintf("========= Processing note %d/%d (total: %d/%d) \"%s\" =========",
				noteNumber, totalNotes, noteNumberAll, totalNotesAll, note.Title),
				"tags", note.Tags)
		} else {
			slog.Info(fmt.Sprintf("========= Processing note %d/%d \"%s\" =========",
				noteNumber, totalNotes, note.Title),
				"tags", note.Tags)
		}

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
					noteTime, hasTime := parseTimestamp(note.Created, "")
					if hasTime {
						helpers.SetFileTimestamps(e.Fs, mdFilePath, noteTime, "markdown file")
					}

					slog.Info("saved note content as markdown", "path", mdFilePath)
				} else {
					// If no output folder is specified, upload the markdown content as a separate document
					documentTitle := note.Title
					mdFileName := helpers.SanitizeFilename(note.Title) + ".md"

					// Create a deep copy of the note for the markdown file
					mdNote := Note{
						Title:          note.Title,
						Content:        note.Content,
						Created:        note.Created,
						Updated:        note.Updated,
						NoteAttributes: note.NoteAttributes,
						Resources:      note.Resources,
					}

					// Create a copy of the tags slice
					mdNote.Tags = make([]string, len(note.Tags))
					copy(mdNote.Tags, note.Tags)

					// Add markdown tag only to the markdown file's note
					mdNote.Tags = append(mdNote.Tags, "markdown")

					// Upload the markdown content as a new document
					_, err := paperless.UploadFile(e.client, documentTitle, mdFileName, "text/markdown", []byte(mdContent), mdNote, url, e.taskTracker, failedNoteChannel)
					if err != nil {
						slog.Error("failed to upload markdown content", "error", err)
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
				slog.Debug("processing resource",
					"resource", resource.ResourceAttributes.FileName,
					"mime", resource.Mime,
					"note", note.Title)

				// Decode the base64 Resource.Data
				decodedData, err := e.decodeBase64ResourceData(resource.Data)
				if err != nil {
					continue
				}

				isIWorkFile := helpers.IsIWorkFile(resource.Mime, resource.ResourceAttributes.FileName)
				if isIWorkFile && !settings.ConvertAppleToPDF && settings.OutputFolder == "" {
					slog.Info("skipping iWork file because ConvertAppleToPDF is not enabled",
						"filename", resource.ResourceAttributes.FileName,
						"mime_type", resource.Mime)
					continue
				}

				// Check if file has no name and should be saved to noname folder
				if resource.ResourceAttributes.FileName == "" && settings.NoNameFolder != "" {
					// Still need to set a filename
					resource.ResourceAttributes.FileName = note.Title

					nonameFolder := settings.NoNameFolder

					// Ensure the noname directory exists
					if err := e.Fs.MkdirAll(nonameFolder, 0755); err != nil {
						slog.Error("failed to create noname directory", "error", err)
					} else {
						slog.Debug("saving file with no original filename to noname folder", "title", note.Title)

						// Save to the noname folder and continue to next resource
						_, err := e.SaveResourceAsFile(nonameFolder, note, resource, decodedData)
						if err != nil {
							failedNoteChannel <- note
							slog.Error("failed to save resource file to noname folder", "error", err)
							break
						}
						continue
					}
				}

				// only process wanted file types
				isAllowed, err := helpers.IsAllowedFileType(resource.Mime, resource.ResourceAttributes.FileName)
				if err != nil {
					slog.Error("error when handling MIME type", "error", err)
					continue
				}

				if !isAllowed {
					slog.Debug("skipping unwanted file type", "filename", resource.ResourceAttributes.FileName, "filetype", resource.Mime)

					// Check if we should save excluded files
					if settings.ExcludedOutputFolder != "" {
						slog.Debug("saving excluded file to excluded output folder",
							"filename", resource.ResourceAttributes.FileName,
							"filetype", resource.Mime,
							"folder", settings.ExcludedOutputFolder)

						// Make sure the excluded output folder exists
						if err := e.Fs.MkdirAll(settings.ExcludedOutputFolder, 0755); err != nil {
							slog.Error("failed to create excluded output directory", "error", err)
							continue
						}

						// Save the excluded file
						_, err = e.SaveResourceAsFile(settings.ExcludedOutputFolder, note, resource, decodedData)
						if err != nil {
							slog.Error("failed to save excluded resource file", "error", err)
							continue
						}
					}

					continue
				}

				// Handle zip files first, regardless of output folder setting
				if settings.Unzip && strings.HasSuffix(strings.ToLower(resource.ResourceAttributes.FileName), ".zip") {
					slog.Info("processing zip file", "file", resource.ResourceAttributes.FileName)

					// Create a reader from the byte slice to inspect zip contents
					zipReader, err := zip.NewReader(bytes.NewReader(decodedData), int64(len(decodedData)))
					if err != nil {
						failedNoteChannel <- note
						slog.Error("failed to create zip reader", "error", err)
						continue
					}

					// Debug output for zip contents
					slog.Debug("zip file contents:", "total_files", len(zipReader.File))
					for _, file := range zipReader.File {
						slog.Debug("zip entry:",
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

					extractedFiles, err := helpers.UnzipFile(decodedData, extractDir, e.Fs, resource.ResourceAttributes.FileName, note.Title)
					if err != nil {
						failedNoteChannel <- note
						slog.Error("failed to extract zip file", "error", err)
						continue
					}

					// Track files for cleanup
					var filesToCleanup []string

					for _, file := range extractedFiles {
						// Check if this file type should be processed
						isAllowed, err := helpers.IsAllowedFileType(file.MimeType, file.Name)
						if err != nil {
							failedNoteChannel <- note
							slog.Error("error when handling MIME type", "error", err)
							continue
						}

						isIWorkFile := helpers.IsIWorkFile(file.MimeType, file.Name)
						if isIWorkFile && !settings.ConvertAppleToPDF && settings.OutputFolder == "" {
							slog.Info("skipping iWork file because ConvertAppleToPDF is not enabled",
								"filename", file.Name,
								"mime_type", file.MimeType)
							continue
						}

						if !isAllowed {
							slog.Debug("skipping unwanted extracted file type", "filename", file.Name, "filetype", file.MimeType)

							// Check if we should save excluded files
							if settings.ExcludedOutputFolder != "" {
								slog.Info("saving excluded extracted file to excluded output folder",
									"filename", file.Name,
									"filetype", file.MimeType,
									"folder", settings.ExcludedOutputFolder)

								// Make sure the excluded output folder exists
								if err := e.Fs.MkdirAll(settings.ExcludedOutputFolder, 0755); err != nil {
									slog.Error("failed to create excluded output directory", "error", err)
									continue
								}

								// Create a filename that includes zip context
								zipInfo := ""
								if file.ZipFileName != "" {
									zipFileNameWithoutExt := strings.TrimSuffix(file.ZipFileName, filepath.Ext(file.ZipFileName))
									zipInfo = zipFileNameWithoutExt + " - "
								}

								// Create full path with context
								excludedFileName := filepath.Join(settings.ExcludedOutputFolder, zipInfo+filepath.Base(file.Name))

								// Handle filename conflicts
								excludedFileName = helpers.EnsureUniqueFilename(e.Fs, excludedFileName)

								// Write the file
								if err := afero.WriteFile(e.Fs, excludedFileName, file.Data, 0644); err != nil {
									slog.Error("failed to save excluded extracted file", "error", err)
									continue
								}

								// Set the timestamp if available
								if !file.FileTime.IsZero() {
									helpers.SetFileTimestamps(e.Fs, excludedFileName, file.FileTime, "excluded extracted file")
								}

								slog.Info("saved excluded extracted file", "path", excludedFileName)
								e.Uploads.Add(1)
							}

							continue
						}

						// Check if this is an Apple iWork file that should be converted to PDF
						// Initialize with original file data
						uploadData := file.Data
						uploadMimeType := file.MimeType
						uploadFileName := file.Name

						// Use the improved handler function for PDF conversion
						convertedData, convertedMimeType, convertedFilename, converted, _ := e.convertIWorkDataToPDF(
							file.Data,
							file.Name,
							file.MimeType,
							note.Created,
							"", // No explicit timestamp string
							file.FileTime,
							outputFolder,
						)

						if converted {
							uploadData = convertedData
							uploadMimeType = convertedMimeType
							uploadFileName = convertedFilename

							// If we're using an output folder, the file is already saved
							if outputFolder != "" {
								// Skip upload to Paperless - we've saved to the file system
								continue
							}
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

						_, err = paperless.UploadFile(e.client, note.Title+" | "+zipFileNameWithoutExt+" | "+fileNameWithoutExt, uploadFileName, uploadMimeType, uploadData, note, url, e.taskTracker, failedNoteChannel)
						if err != nil {
							failedNoteChannel <- note
							slog.Error("failed to upload extracted file", "error", err)
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

				if resource.ResourceAttributes.FileName == "" {
					// Still need to set a filename
					resource.ResourceAttributes.FileName = note.Title
				}

				// if outputFolder is set, output to disk and continue
				if outputFolder != "" {
					_, err := e.SaveResourceAsFile(outputFolder, note, resource, decodedData)
					if err != nil {
						failedNoteChannel <- note
						slog.Error("failed to save resource file", "error", err)
						break
					}
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

				// Initialize upload variables
				uploadData := decodedData
				uploadMimeType := resource.Mime
				uploadFileName := resource.ResourceAttributes.FileName

				// Skip upload if we're using an output folder
				if outputFolder != "" {
					slog.Debug("skipping upload - using output folder",
						"file", uploadFileName,
						"output_folder", outputFolder)
					continue
				}

				// Handle PDF conversion if needed
				fileTime, _ := parseTimestamp(resource.ResourceAttributes.Timestamp, note.Created)
				convertedData, convertedMimeType, convertedFilename, converted, _ := e.convertIWorkDataToPDF(
					decodedData,
					resource.ResourceAttributes.FileName,
					resource.Mime,
					note.Created,
					resource.ResourceAttributes.Timestamp,
					fileTime,
					outputFolder,
				)

				if converted {
					uploadData = convertedData
					uploadMimeType = convertedMimeType
					uploadFileName = convertedFilename

					slog.Info("successfully converted file to PDF before upload",
						"original_file", resource.ResourceAttributes.FileName,
						"pdf_file", uploadFileName,
						"pdf_size", len(uploadData))
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

	return nil // no error
}

// ProcessPendingTasks processes all pending tasks and links documents
// This should be called only once after all workers have finished
func (e *EnexFile) ProcessPendingTasks() error {
	settings, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("error getting config: %v", err)
	}

	// Ensure HTTP client is initialized
	if e.client == nil {
		e.client = &http.Client{
			Timeout: time.Second * 30, // Use a longer timeout for task processing
		}
		slog.Debug("HTTP client initialized")
	}

	// Check if task tracker is initialized
	if e.taskTracker == nil {
		slog.Debug("Task tracker not initialized, initializing now")

		// Initialize with options
		tracker, err := paperless.InitTaskTracker(paperless.TaskTrackerOptions{
			File:           "tasks.json",
			Fs:             e.Fs,
			CheckLinkField: true,
		})

		if err != nil {
			return fmt.Errorf("failed to initialize task tracker: %v", err)
		}
		e.taskTracker = tracker
	}

	// Only if we're not using an output folder and we have a task tracker
	if settings.OutputFolder == "" && settings.LinkFieldID > 0 && e.taskTracker != nil {
		slog.Info("processing all pending tasks")
		if err := paperless.ProcessPendingTasks(e.taskTracker, e.client); err != nil {
			slog.Error("failed to process pending tasks", "error", err)
			return err
		}

		// Link all documents after all tasks are processed
		slog.Info("linking all documents")
		if err := paperless.LinkDocumentsFromTasks(e.taskTracker, e.client); err != nil {
			slog.Error("failed to link documents", "error", err)
			return err
		}
	} else if settings.OutputFolder != "" {
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
	for i, note := range *failedNotes {
		slog.Debug(fmt.Sprintf("feeding failed note %d to retry channel", i+1))
		retryChannel <- note
	}
	close(retryChannel)
}

// Client returns the HTTP client used by this EnexFile
func (e *EnexFile) Client() *http.Client {
	return e.client
}

// SetClient sets the HTTP client for this EnexFile
func (e *EnexFile) SetClient(client *http.Client) {
	e.client = client
}

// CountNotes counts the total number of notes in an ENEX file without processing them
func (e *EnexFile) CountNotes(filePath string) (int, error) {
	slog.Debug(fmt.Sprintf("counting notes in file: %v", filePath))
	file, err := e.Fs.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	decoder.Strict = false

	count := 0
	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Log this error but continue counting
			slog.Error("XML parsing error while counting notes", "error", err)
			break
		}
		switch se := t.(type) {
		case xml.StartElement:
			if se.Name.Local == "note" {
				count++
				// Skip to the end of this note element to avoid parsing its contents
				if err := decoder.Skip(); err != nil {
					slog.Error("error skipping note element", "error", err)
				}
			}
		}
	}
	slog.Debug("note count in ENEX file", "total_notes", count, "file", filePath)
	return count, nil
}
