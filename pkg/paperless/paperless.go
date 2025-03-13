package paperless

import (
	"bytes"
	"encoding/json"
	"enex2paperless/internal/config"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func GetTagID(tagName string) (int, error) {
	settings, _ := config.GetConfig()

	// Use HTTP client to send GET request
	url := fmt.Sprintf("%v/api/tags/?name__iexact=%s", settings.PaperlessAPI, url.QueryEscape(tagName))

	req, err := http.NewRequest("GET", url, nil)

	// auth
	if settings.Token != "" {
		req.Header.Set("Authorization", "Token "+settings.Token)
	} else {
		req.SetBasicAuth(settings.Username, settings.Password)
	}

	// Send the request
	slog.Debug("sending GET request")

	slog.Debug("request details",
		"method", req.Method,
		"url", req.URL.String(),
		"headers", req.Header)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve tags: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)

		slog.Error("non 200 status code received", "status code", resp.StatusCode, "body", buf.String())

		return 0, fmt.Errorf("non 200 status code")
	}

	var tagResponse struct {
		Count   int `json:"count"`
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tagResponse); err != nil {
		return 0, fmt.Errorf("failed to decode response: %v", err)
	}

	if tagResponse.Count == 0 {
		slog.Debug("no tag found with name", "name", tagName)
		return 0, nil // Tag not found, but not an error
	}

	return tagResponse.Results[0].ID, nil // Return the ID of the first matching tag
}

func CreateTag(tagName string) (int, error) {
	settings, _ := config.GetConfig()

	url := fmt.Sprintf("%v/api/tags/", settings.PaperlessAPI)
	jsonData, err := json.Marshal(map[string]interface{}{
		"name": tagName,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to marshal JSON: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %v", err)
	}

	// auth
	if settings.Token != "" {
		req.Header.Set("Authorization", "Token "+settings.Token)
	} else {
		req.SetBasicAuth(settings.Username, settings.Password)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Debug("request details",
		"method", req.Method,
		"url", req.URL.String(),
		"headers", req.Header,
		"body", string(jsonData))

	// send request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		// If creation failed, the tag might have been created by another goroutine
		// Try to get the tag ID again
		id, err := GetTagID(tagName)
		if err != nil {
			return 0, fmt.Errorf("failed to create tag and couldn't verify if it exists: %v", err)
		}
		if id != 0 {
			// Tag exists now, probably created by another goroutine
			slog.Debug("tag was created by another process", "tag", tagName, "id", id)
			return id, nil
		}

		// If we still can't find the tag, then there's a real error
		slog.Error("non 201 status code received", "status code", resp.StatusCode)

		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		slog.Error("response:", "body", buf.String())

		return 0, fmt.Errorf("failed to create tag")
	}

	// read response
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response body: %v", err)
	}
	// slog.Debug("Response Body", "body", string(bodyBytes))

	// Unmarshal the response to get the tag ID
	var tagResponse TagResponse
	err = json.Unmarshal(bodyBytes, &tagResponse)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal response: %v", err)
	}
	return tagResponse.ID, nil
}

type TagResponse struct {
	ID int `json:"id"`
	// Other fields, if necessary
}

func ConvertDateFormat(dateStr string) (string, error) {
	// Parse the original date string into a time.Time
	parsedTime, err := time.Parse("20060102T150405Z", dateStr)
	if err != nil {
		return "", fmt.Errorf("error parsing time: %v", err)
	}

	// Convert time.Time to the desired string format
	return parsedTime.Format("2006-01-02 15:04:05-07:00"), nil
}

// GetCorrespondentID retrieves the ID of a correspondent by name
func GetCorrespondentID(correspondentName string) (int, error) {
	settings, _ := config.GetConfig()

	// Use HTTP client to send GET request
	url := fmt.Sprintf("%v/api/correspondents/?name__iexact=%s", settings.PaperlessAPI, url.QueryEscape(correspondentName))

	req, err := http.NewRequest("GET", url, nil)

	// auth
	if settings.Token != "" {
		req.Header.Set("Authorization", "Token "+settings.Token)
	} else {
		req.SetBasicAuth(settings.Username, settings.Password)
	}

	// Send the request
	slog.Debug("sending GET request for correspondent")

	slog.Debug("request details",
		"method", req.Method,
		"url", req.URL.String(),
		"headers", req.Header)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve correspondents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)

		slog.Error("non 200 status code received", "status code", resp.StatusCode, "body", buf.String())

		return 0, fmt.Errorf("non 200 status code")
	}

	var correspondentResponse struct {
		Count   int `json:"count"`
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&correspondentResponse); err != nil {
		return 0, fmt.Errorf("failed to decode response: %v", err)
	}

	if correspondentResponse.Count == 0 {
		slog.Debug("no correspondent found with name", "name", correspondentName)
		return 0, nil // Correspondent not found, but not an error
	}

	return correspondentResponse.Results[0].ID, nil // Return the ID of the first matching correspondent
}

// CreateCorrespondent creates a new correspondent with the given name
func CreateCorrespondent(correspondentName string) (int, error) {
	settings, _ := config.GetConfig()

	url := fmt.Sprintf("%v/api/correspondents/", settings.PaperlessAPI)
	jsonData, err := json.Marshal(map[string]interface{}{
		"name": correspondentName,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to marshal JSON: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %v", err)
	}

	// auth
	if settings.Token != "" {
		req.Header.Set("Authorization", "Token "+settings.Token)
	} else {
		req.SetBasicAuth(settings.Username, settings.Password)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Debug("request details",
		"method", req.Method,
		"url", req.URL.String(),
		"headers", req.Header,
		"body", string(jsonData))

	// send request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		// If creation failed, the correspondent might have been created by another goroutine
		// Try to get the correspondent ID again
		id, err := GetCorrespondentID(correspondentName)
		if err != nil {
			return 0, fmt.Errorf("failed to create correspondent and couldn't verify if it exists: %v", err)
		}
		if id != 0 {
			// Correspondent exists now, probably created by another goroutine
			slog.Debug("correspondent was created by another process", "correspondent", correspondentName, "id", id)
			return id, nil
		}

		// If we still can't find the correspondent, then there's a real error
		slog.Error("non 201 status code received", "status code", resp.StatusCode)

		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		slog.Error("response:", "body", buf.String())

		return 0, fmt.Errorf("failed to create correspondent")
	}

	// read response
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response body: %v", err)
	}

	// Unmarshal the response to get the correspondent ID
	var correspondentResponse CorrespondentResponse
	err = json.Unmarshal(bodyBytes, &correspondentResponse)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal response: %v", err)
	}
	return correspondentResponse.ID, nil
}

type CorrespondentResponse struct {
	ID int `json:"id"`
	// Other fields, if necessary
}

// FormatCorrespondentName formats a correspondent name to have the first letter of each word capitalized
func FormatCorrespondentName(name string) string {
	words := strings.Fields(name)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[0:1]) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, " ")
}

// UploadFile uploads a file to Paperless and returns the document ID
func UploadFile(client *http.Client, title string, fileName string, mimeType string, data []byte, note interface{}, url string, taskTracker interface{}, failedNoteChannel interface{}) (int, error) {
	// Create a new buffer and multipart writer for form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	var bodyBytes []byte
	var docIDStr string

	// Cast the note interface to the expected type
	noteObj, ok := note.(enexNote)
	if !ok {
		slog.Error("note is not of expected type")
		// We don't try to directly cast the failedNoteChannel since that's causing the error
		return 0, fmt.Errorf("note is not of expected type")
	}

	// Don't try to cast the failedNoteChannel to a specific type
	// Instead, we'll check its concrete type when we need to use it

	// Set form fields
	err := writer.WriteField("title", title)
	if err != nil {
		// Handle the error case for the failedNoteChannel
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error setting form fields", "error", err)
		return 0, fmt.Errorf("error setting form fields: %v", err)
	}

	formattedCreatedDate, err := ConvertDateFormat(noteObj.GetCreated())
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error converting date format", "error", err)
		return 0, fmt.Errorf("error converting date format: %v", err)
	}
	_ = writer.WriteField("created", formattedCreatedDate)

	// Get settings for additional tags and correspondent tag prefix
	settings, err := config.GetConfig()
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("failed to get config", "error", err)
		return 0, fmt.Errorf("failed to get config: %v", err)
	}

	// Check for correspondent tag prefix
	var correspondentID int
	var tagsToProcess []string
	var correspondentTagFound bool

	if settings.CorrespondentTagPrefix != "" {
		for _, tagName := range noteObj.GetTags() {
			if strings.HasPrefix(tagName, settings.CorrespondentTagPrefix) {
				if !correspondentTagFound {
					// This is the first tag with the prefix, use it as correspondent
					correspondentTagFound = true

					// Remove the prefix and format the correspondent name
					correspondentName := strings.TrimPrefix(tagName, settings.CorrespondentTagPrefix)
					formattedName := FormatCorrespondentName(correspondentName)

					// Get or create correspondent
					id, err := GetCorrespondentID(formattedName)
					if err != nil {
						sendToFailedChannel(failedNoteChannel, note)
						slog.Error("failed to check for correspondent", "error", err)
						return 0, fmt.Errorf("failed to check for correspondent: %v", err)
					}

					if id == 0 {
						slog.Debug("creating correspondent", "correspondent", formattedName)
						id, err = CreateCorrespondent(formattedName)
						if err != nil {
							sendToFailedChannel(failedNoteChannel, note)
							slog.Error("couldn't create correspondent", "error", err)
							return 0, fmt.Errorf("couldn't create correspondent: %v", err)
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
		tagsToProcess = noteObj.GetTags()
	}

	// Set correspondent ID if found
	if correspondentID > 0 {
		err = writer.WriteField("correspondent", strconv.Itoa(correspondentID))
		if err != nil {
			sendToFailedChannel(failedNoteChannel, note)
			slog.Error("couldn't write correspondent field", "error", err)
			return 0, fmt.Errorf("couldn't write correspondent field: %v", err)
		}
	}

	// Get or create tag IDs
	var tagIDs []int

	// Combine processed tags and additional tags into one slice to process
	allTags := append([]string{}, tagsToProcess...)
	if len(settings.AdditionalTags) > 0 {
		allTags = append(allTags, settings.AdditionalTags...)
	}

	for _, tagName := range allTags {
		id, err := GetTagID(tagName)
		if err != nil {
			sendToFailedChannel(failedNoteChannel, note)
			slog.Error("failed to check for tag", "error", err)
			return 0, fmt.Errorf("failed to check for tag: %v", err)
		}

		if id == 0 {
			slog.Debug("creating tag", "tag", tagName)
			id, err = CreateTag(tagName)
			if err != nil {
				sendToFailedChannel(failedNoteChannel, note)
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
			sendToFailedChannel(failedNoteChannel, note)
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
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error creating multipart writer", "error", err)
		return 0, fmt.Errorf("error creating multipart writer: %v", err)
	}

	_, err = io.Copy(part, bytes.NewReader(data))
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error writing file data", "error", err)
		return 0, fmt.Errorf("error writing file data: %v", err)
	}

	// Close the writer to finish the multipart content
	writer.Close()

	// Create a new HTTP request
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
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

	resp, err := client.Do(req)
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error making POST request", "error", err)
		return 0, fmt.Errorf("error making POST request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// print response body
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("non 200 status code received", "status code", resp.StatusCode)
		slog.Error("response:", "body", buf.String())
		return 0, fmt.Errorf("non 200 status code received (%d): %s", resp.StatusCode, buf.String())
	}

	// Read the response body
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error reading response body", "error", err)
		return 0, fmt.Errorf("error reading response body: %v", err)
	}

	// Try to unmarshal as a string first (UUID)
	if err := json.Unmarshal(bodyBytes, &docIDStr); err == nil {
		slog.Debug("Response is a string",
			"id", docIDStr,
			"title", title,
			"filename", fileName)

		// Only create task info if linking is enabled and taskTracker is provided
		if settings.LinkFieldID > 0 && taskTracker != nil {
			// Create task info
			taskInfo := TaskInfo{
				TaskID:      docIDStr,
				Title:       title,
				FileName:    fileName,
				NoteTitle:   noteObj.GetTitle(),
				Status:      "PENDING",
				DateCreated: time.Now().Format(time.RFC3339),
			}

			// Try to add the task to the tracker using a safer approach
			if err := addTaskToTracker(taskTracker, taskInfo); err != nil {
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
		sendToFailedChannel(failedNoteChannel, note)
		slog.Error("error decoding document details", "error", err)
		return 0, fmt.Errorf("error decoding document details: %v", err)
	}

	if id, ok := docDetails["id"].(float64); ok {
		slog.Debug("Found document ID in response",
			"id", id,
			"title", title)
		// We can't access e.Uploads here anymore
		return int(id), nil
	}

	sendToFailedChannel(failedNoteChannel, note)
	slog.Error("no document ID found in response",
		"response", docDetails,
		"title", title)
	return 0, fmt.Errorf("no document ID found in response")
}

// Helper function to safely send to the failed channel
func sendToFailedChannel(failedNoteChannel interface{}, note interface{}) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic when sending to failed channel", "error", r)
		}
	}()

	// Try different channel types - we support the specific channel types
	// that might be used by callers
	switch fc := failedNoteChannel.(type) {
	case chan interface{}:
		fc <- note
	default:
		// Use reflection as a last resort for channels of specific types
		if failedNoteChannel != nil {
			channelValue := reflect.ValueOf(failedNoteChannel)
			if channelValue.Kind() == reflect.Chan && !channelValue.IsNil() {
				noteValue := reflect.ValueOf(note)
				// Safely try to send to the channel using reflection
				if noteValue.Type().AssignableTo(channelValue.Type().Elem()) {
					channelValue.Send(noteValue)
				} else {
					slog.Error("failed to send to channel: note type doesn't match channel element type",
						"note_type", noteValue.Type(),
						"channel_element_type", channelValue.Type().Elem())
				}
			}
		}
	}
}

// Helper function to safely add a task to the tracker
func addTaskToTracker(taskTracker interface{}, taskInfo TaskInfo) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic when adding task to tracker", "error", r)
		}
	}()

	// Try direct type assertion first
	if tracker, ok := taskTracker.(TaskTracker); ok {
		return tracker.AddTask(taskInfo)
	}

	// Fall back to reflection
	if taskTracker != nil {
		trackerValue := reflect.ValueOf(taskTracker)

		// Check if the tracker is a pointer and not nil
		if trackerValue.Kind() == reflect.Ptr && !trackerValue.IsNil() {
			// Look for an AddTask method
			addTaskMethod := trackerValue.MethodByName("AddTask")
			if addTaskMethod.IsValid() && !addTaskMethod.IsNil() {
				// We need to convert our TaskInfo to a compatible type
				methodType := addTaskMethod.Type()
				if methodType.NumIn() == 1 {
					// Get the expected parameter type
					expectedParamType := methodType.In(0)
					slog.Debug("Expected parameter type for AddTask",
						"type", expectedParamType.String(),
						"our_type", reflect.TypeOf(taskInfo).String())

					// Create a value of the expected parameter type and copy fields from our TaskInfo
					paramValue := reflect.New(expectedParamType).Elem()

					// Try to copy common fields
					ourTaskInfoValue := reflect.ValueOf(taskInfo)
					ourTaskInfoType := ourTaskInfoValue.Type()

					// Copy fields by name rather than by position
					for i := 0; i < ourTaskInfoType.NumField(); i++ {
						fieldName := ourTaskInfoType.Field(i).Name

						// Check if this field exists in the target type
						targetField := paramValue.FieldByName(fieldName)
						if targetField.IsValid() && targetField.CanSet() {
							// Get source field value
							sourceField := ourTaskInfoValue.Field(i)

							// Check if the types are compatible
							if sourceField.Type().AssignableTo(targetField.Type()) {
								targetField.Set(sourceField)
							} else {
								slog.Debug("Field types not directly assignable",
									"field", fieldName,
									"source_type", sourceField.Type(),
									"target_type", targetField.Type())
							}
						}
					}

					// Call the method with our converted parameter
					results := addTaskMethod.Call([]reflect.Value{paramValue})

					// Check for error return
					if len(results) > 0 && results[0].Kind() == reflect.Interface {
						if !results[0].IsNil() && results[0].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
							return results[0].Interface().(error)
						}
					}

					return nil // Method called successfully
				}
			}
		}
	}

	// If no proper AddTask method was found, log it but don't fail the operation
	slog.Warn("taskTracker does not implement required AddTask method, task information won't be stored",
		"task_id", taskInfo.TaskID,
		"title", taskInfo.Title)
	return nil
}

// Define interfaces needed for UploadFile
type enexNote interface {
	// Define the minimum interface requirements for a Note
	GetTitle() string
	GetCreated() string
	GetTags() []string
}

// TaskInfo represents information about a document upload task and its linking data
type TaskInfo struct {
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	FileName      string   `json:"file_name"`
	NoteTitle     string   `json:"note_title"`
	DocumentID    int      `json:"document_id,omitempty"`
	Status        string   `json:"status"`
	RelatedDocIDs []int    `json:"related_doc_ids,omitempty"`
	DateCreated   string   `json:"date_created"`
	DateDone      string   `json:"date_done,omitempty"`
}

// TaskTracker interface defines methods needed for task tracking
type TaskTracker interface {
	AddTask(task TaskInfo) error
}
